CREATE TABLE IF NOT EXISTS orders (id serial PRIMARY KEY, item text NOT NULL, created timestamptz DEFAULT now());
INSERT INTO orders (item) VALUES ('coffee'), ('tea'), ('cake');
