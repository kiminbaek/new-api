// model-priority-page.go — [CUSTOM] 自包含模型优先级看板 HTML 页面
// 不走 React SPA，直接挂 /admin/model-priority 页，调 /api/channel/model-priority 渲染表格。
package router

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const modelPriorityPageHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>模型优先级看板</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:#f8fafc;color:#1e293b;padding:20px}
h1{font-size:1.5rem;margin-bottom:16px;display:flex;align-items:center;gap:8px}
.refresh{background:#3b82f6;color:#fff;border:none;padding:8px 16px;border-radius:6px;cursor:pointer;font-size:0.875rem}
.refresh:hover{background:#2563eb}
.summary{display:flex;gap:12px;margin-bottom:16px;flex-wrap:wrap}
.card{background:#fff;padding:12px 16px;border-radius:8px;box-shadow:0 1px 3px rgba(0,0,0,0.1);min-width:140px}
.card .num{font-size:1.5rem;font-weight:700}
.card .label{font-size:0.75rem;color:#64748b;margin-top:2px}
table{width:100%;border-collapse:collapse;background:#fff;border-radius:8px;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,0.1)}
th{background:#f1f5f9;padding:10px 8px;text-align:left;font-size:0.75rem;text-transform:uppercase;color:#64748b;cursor:pointer;user-select:none}
th:hover{background:#e2e8f0}
td{padding:8px;border-top:1px solid #f1f5f9;font-size:0.875rem}
tr:hover{background:#f8fafc}
.delta-neg{color:#dc2626;font-weight:600}
.delta-pos{color:#16a34a;font-weight:600}
.delta-zero{color:#94a3b8}
.badge{display:inline-block;padding:2px 8px;border-radius:4px;font-size:0.75rem;font-weight:500}
.badge-on{background:#dcfce7;color:#16a34a}
.badge-off{background:#fee2e2;color:#dc2626}
.filter{margin-bottom:12px;display:flex;gap:8px;flex-wrap:wrap}
.filter input,.filter select{padding:6px 10px;border:1px solid #cbd5e1;border-radius:6px;font-size:0.875rem}
.loading{text-align:center;padding:40px;color:#64748b}
.empty{text-align:center;padding:40px;color:#94a3b8}
</style>
</head>
<body>
<h1>📊 模型优先级看板 <button class="refresh" onclick="load()">刷新</button></h1>
<div class="summary" id="summary"></div>
<div class="filter">
  <input id="f-model" placeholder="筛模型名..." oninput="render()">
  <input id="f-channel" placeholder="筛渠道名..." oninput="render()">
  <select id="f-delta" onchange="render()">
    <option value="">全部偏移</option>
    <option value="neg">仅降权(&minus;)</option>
    <option value="pos">仅升权(+)</option>
    <option value="zero">仅无变化</option>
  </select>
  <select id="f-enabled" onchange="render()">
    <option value="">全部状态</option>
    <option value="on">仅启用</option>
    <option value="off">仅禁用</option>
  </select>
</div>
<div id="table"></div>
<script>
let rows=[],sortBy='model',sortDir=1;
async function load(){
  const t=document.getElementById('table');
  t.innerHTML='<div class="loading">加载中...</div>';
  // 取 token：优先 localStorage 的 session token
  let token=localStorage.getItem('session')||localStorage.getItem('token')||'';
  if(token&&token.startsWith('"'))token=JSON.parse(token);
  const r=await fetch('/api/channel/model-priority',{headers:{'Authorization':'Bearer '+token,'New-Api-User':'1'}});
  const j=await r.json();
  if(!j.success){t.innerHTML='<div class="empty">'+(j.message||'加载失败')+'</div>';return;}
  rows=j.data||[];
  document.getElementById('summary').innerHTML=[
    {n:rows.length,l:'总条目'},
    {n:rows.filter(x=>x.delta<0).length,l:'降权中'},
    {n:rows.filter(x=>x.delta>0).length,l:'升权中'},
    {n:rows.filter(x=>x.delta!==0).length,l:'自动调整'},
  ].map(c=>'<div class="card"><div class="num">'+c.n+'</div><div class="label">'+c.l+'</div></div>').join('');
  render();
}
function render(){
  let fm=document.getElementById('f-model').value.toLowerCase();
  let fc=document.getElementById('f-channel').value.toLowerCase();
  let fd=document.getElementById('f-delta').value;
  let fe=document.getElementById('f-enabled').value;
  let d=rows.filter(x=>{
    if(fm&&!x.model.toLowerCase().includes(fm))return false;
    if(fc&&!x.channel_name.toLowerCase().includes(fc))return false;
    if(fd==='neg'&&x.delta>=0)return false;
    if(fd==='pos'&&x.delta<=0)return false;
    if(fd==='zero'&&x.delta!==0)return false;
    if(fe==='on'&&!x.enabled)return false;
    if(fe==='off'&&x.enabled)return false;
    return true;
  });
  d.sort((a,b)=>{
    let va=a[sortBy],vb=b[sortBy];
    if(typeof va==='string')return va.localeCompare(vb)*sortDir;
    return(va-vb)*sortDir;
  });
  if(!d.length){document.getElementById('table').innerHTML='<div class="empty">无匹配数据</div>';return;}
  let h='<table><thead><tr>'+
    ['channel_id','channel_name','model','base_priority','eff_priority','delta','weight','enabled'].map(k=>{
      let label={channel_id:'#',channel_name:'渠道',model:'模型',base_priority:'基准',eff_priority:'有效值',delta:'偏移',weight:'权重',enabled:'状态'}[k];
      return '<th onclick="setSort(\''+k+'\')">'+label+(sortBy===k?(sortDir>0?' ↑':' ↓'):'')+'</th>';
    }).join('')+'</tr></thead><tbody>';
  for(let x of d){
    let dc=x.delta<0?'delta-neg':x.delta>0?'delta-pos':'delta-zero';
    let ds=(x.delta>0?'+':'')+x.delta;
    let en=x.enabled?'<span class="badge badge-on">启用</span>':'<span class="badge badge-off">禁用</span>';
    h+='<tr><td>'+x.channel_id+'</td><td>'+esc(x.channel_name)+'</td><td>'+esc(x.model)+'</td>'+
      '<td>'+x.base_priority+'</td><td>'+x.eff_priority+'</td>'+
      '<td class="'+dc+'">'+ds+'</td><td>'+x.weight+'</td><td>'+en+'</td></tr>';
  }
  h+='</tbody></table>';
  document.getElementById('table').innerHTML=h;
}
function esc(s){return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')}
function setSort(k){if(sortBy===k)sortDir=-sortDir;else{sortBy=k;sortDir=1}render()}
load();
</script>
</body>
</html>`

// RegisterModelPriorityPage 挂载自包含 HTML 页面到 /admin/model-priority
func RegisterModelPriorityPage(router *gin.Engine) {
	router.GET("/admin/model-priority", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(modelPriorityPageHTML))
	})
}
