package sql

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/goxang/rig/core"
)

// Host is the rig service the database runs in: the one svc:// in addr names.
func (d *DB) Host() string {
	rest, ok := strings.CutPrefix(d.opt.Addr, "svc://")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, ":")
	return name
}

func (d *DB) port() string {
	_, port, err := net.SplitHostPort(strings.TrimPrefix(d.opt.Addr, "svc://"))
	if err != nil {
		return map[string]string{"postgres": "5432", "mysql": "3306", "mssql": "1433"}[d.opt.Driver]
	}
	return port
}

func (d *DB) dbName() string {
	if d.opt.Database != "" {
		return d.opt.Database
	}
	return map[string]string{"postgres": "postgres", "mssql": "master"}[d.opt.Driver]
}

// tool is a dump or restore program and how to call it against host:port.
type tool struct {
	name string
	args func(host, port string) []string
	env  []string
}

func (d *DB) dumpTool() tool {
	db := d.dbName()
	if d.opt.Driver == "mysql" {
		return tool{"mysqldump", func(h, p string) []string {
			return []string{"--single-transaction", "--routines", "--triggers", "-h", h, "-P", p, "-u", d.opt.User, db}
		}, []string{"MYSQL_PWD=" + d.opt.Password}}
	}
	return tool{"pg_dump", func(h, p string) []string {
		return []string{"-Fc", "--no-owner", "-h", h, "-p", p, "-U", d.opt.User, "-d", db}
	}, []string{"PGPASSWORD=" + d.opt.Password}}
}

func (d *DB) restoreTool() tool {
	db := d.dbName()
	if d.opt.Driver == "mysql" {
		return tool{"mysql", func(h, p string) []string { return []string{"-h", h, "-P", p, "-u", d.opt.User, db} }, []string{"MYSQL_PWD=" + d.opt.Password}}
	}
	return tool{"pg_restore", func(h, p string) []string {
		return []string{"--clean", "--if-exists", "--no-owner", "--single-transaction", "-h", h, "-p", p, "-U", d.opt.User, "-d", db}
	}, []string{"PGPASSWORD=" + d.opt.Password}}
}

// Snapshot dumps the database into file: pg_dump or mysqldump on this machine, or inside the
// database's container when this machine has none (or a different version); SQL Server backs up to
// its own disk and the file is copied out of its container.
func (d *DB) Snapshot(ctx context.Context, file string, log io.Writer) error {
	if d.opt.Driver == "mssql" {
		return d.mssqlBackup(ctx, file, log)
	}
	return d.native(ctx, d.dumpTool(), func() (io.Reader, io.Writer, func() error, error) {
		f, err := os.Create(file)
		return nil, f, func() error { return f.Close() }, err
	}, log)
}

// Restore replaces the database's contents with file's.
func (d *DB) Restore(ctx context.Context, file string, log io.Writer) error {
	if err := core.Writable(d.env); err != nil {
		return err
	}
	if d.opt.Driver == "mssql" {
		return d.mssqlRestore(ctx, file, log)
	}
	d.closePool(d.dbName())
	return d.native(ctx, d.restoreTool(), func() (io.Reader, io.Writer, func() error, error) {
		f, err := os.Open(file)
		return f, nil, func() error { return f.Close() }, err
	}, log)
}

// native runs t here when this machine has it, else (or when that fails) inside the database's
// container; open gives each attempt fresh stdin/stdout.
func (d *DB) native(ctx context.Context, t tool, open func() (io.Reader, io.Writer, func() error, error), log io.Writer) error {
	var hostErr error
	if _, err := exec.LookPath(t.name); err == nil {
		hostErr = d.attempt(open, func(in io.Reader, out io.Writer, stderr io.Writer) error {
			addr, err := d.env.Resolve(ctx, d.opt.Addr)
			if err != nil {
				return err
			}
			host, port, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(addr, "tcp://"), "http://"))
			if err != nil {
				return err
			}
			fmt.Fprintf(log, "%s on this machine, against %s\n", t.name, addr)
			cmd := exec.CommandContext(ctx, t.name, t.args(host, port)...)
			cmd.Env = append(os.Environ(), t.env...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, stderr
			return cmd.Run()
		})
		if hostErr == nil {
			return nil
		}
	}
	svc := d.Host()
	if svc == "" {
		if hostErr != nil {
			return hostErr
		}
		return fmt.Errorf("%s is not installed here, and %s is not a rig service whose container has it: install %s", t.name, d.opt.Addr, t.name)
	}
	if hostErr != nil {
		fmt.Fprintf(log, "%v; trying inside %s\n", hostErr, svc)
	}
	return d.attempt(open, func(in io.Reader, out io.Writer, stderr io.Writer) error {
		fmt.Fprintf(log, "%s inside %s\n", t.name, svc)
		return d.inContainer(ctx, svc, append(append(append([]string{"env"}, t.env...), t.name), t.args("127.0.0.1", d.port())...), in, out, stderr)
	})
}

func (d *DB) attempt(open func() (io.Reader, io.Writer, func() error, error), run func(in io.Reader, out io.Writer, stderr io.Writer) error) error {
	in, out, done, err := open()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	err = run(in, out, &stderr)
	if cerr := done(); err == nil {
		err = cerr
	}
	if err != nil && stderr.Len() > 0 {
		err = fmt.Errorf("%w: %s", err, lastLines(stderr.String(), 5))
	}
	return err
}

func (d *DB) inContainer(ctx context.Context, svc string, argv []string, in io.Reader, out, stderr io.Writer) error {
	rt, s, err := d.env.Owner(svc)
	if err != nil {
		return err
	}
	if out == nil {
		out = io.Discard
	}
	return rt.Exec(ctx, s, core.ExecOptions{Command: argv, Stdin: in, Stdout: out, Stderr: stderr})
}

// mssqlBackup has the server write a copy-only backup to its own /tmp, then copies it out of its container.
func (d *DB) mssqlBackup(ctx context.Context, file string, log io.Writer) error {
	svc := d.Host()
	if svc == "" {
		return errors.New("SQL Server backs up to its own disk: a snapshot needs it in a rig service's container (addr: svc://...)")
	}
	db, tmp := d.dbName(), fmt.Sprintf("/tmp/rig-%d.bak", time.Now().UnixNano())
	fmt.Fprintf(log, "BACKUP DATABASE [%s] inside %s\n", db, svc)
	if _, err := d.exec(ctx, "master", fmt.Sprintf("BACKUP DATABASE [%s] TO DISK = N'%s' WITH COPY_ONLY, INIT", db, tmp)); err != nil {
		return err
	}
	defer d.inContainer(context.WithoutCancel(ctx), svc, []string{"rm", "-f", tmp}, nil, nil, io.Discard) //nolint:errcheck
	f, err := os.Create(file)
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	err = d.inContainer(ctx, svc, []string{"cat", tmp}, nil, f, &stderr)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("copy the backup out of %s: %w %s", svc, err, stderr.String())
	}
	return nil
}

func (d *DB) mssqlRestore(ctx context.Context, file string, log io.Writer) error {
	svc := d.Host()
	if svc == "" {
		return errors.New("SQL Server restores from its own disk: a restore needs it in a rig service's container (addr: svc://...)")
	}
	db, tmp := d.dbName(), fmt.Sprintf("/tmp/rig-%d.bak", time.Now().UnixNano())
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(log, "copying %s into %s\n", filepath.Base(file), svc)
	var stderr bytes.Buffer
	if err := d.inContainer(ctx, svc, []string{"sh", "-c", "cat > " + tmp}, f, nil, &stderr); err != nil {
		return fmt.Errorf("copy the backup into %s: %w %s", svc, err, stderr.String())
	}
	defer d.inContainer(context.WithoutCancel(ctx), svc, []string{"rm", "-f", tmp}, nil, nil, io.Discard) //nolint:errcheck
	d.closePool(db)
	fmt.Fprintf(log, "RESTORE DATABASE [%s]\n", db)
	_, err = d.exec(ctx, "master", fmt.Sprintf("IF DB_ID('%[1]s') IS NOT NULL ALTER DATABASE [%[1]s] SET SINGLE_USER WITH ROLLBACK IMMEDIATE\n"+
		"GO\nRESTORE DATABASE [%[1]s] FROM DISK = N'%[2]s' WITH REPLACE\nGO\nALTER DATABASE [%[1]s] SET MULTI_USER", db, tmp))
	return err
}

func (d *DB) closePool(db string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p, ok := d.pools[db]; ok {
		_ = p.Close()
		delete(d.pools, db)
	}
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, " | ")
}
