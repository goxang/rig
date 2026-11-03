const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const ctx=vm.createContext({URL,location:{href:'http://127.0.0.1:7979/',origin:'http://127.0.0.1:7979'},h:(tag,props,...children)=>({tag,props,children})});
vm.runInContext(fs.readFileSync(__dirname+'/static/widgets.js','utf8'),ctx);
const render=url=>vm.runInContext(`simpleWidget('web',{props:{title:'Editor'}},{fields:[],rows:[{url:${JSON.stringify(url)}}]})`,ctx);
test('web plugins are isolated loopback frames',()=>{
 const result=render('http://127.0.0.1:22123/token/index.html');
 assert.equal(result.tag,'iframe');assert.equal(result.props.title,'Editor');
 assert.equal(result.props.sandbox,'allow-scripts allow-same-origin allow-downloads allow-modals');
 for(const url of ['https://example.com','javascript:alert(1)','http://localhost:22123/','http://127.0.0.1:7979/','https://127.0.0.1:22123/'])assert.equal(render(url).tag,'div',url);
});
