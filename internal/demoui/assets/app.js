'use strict';
const el = id => document.getElementById(id);
let operator = '', subjects = [], selected = null, voice = null, chat = null, candidate = null, envelope = null, pending = null, busy = false, retryTurn = null, retryRequirement = null;
const errors = { unauthorized:'The operator token was rejected.', session_unavailable:'This session is unavailable or expired. Start a fresh session.', memory_refresh_pending:'Memory is refreshing. Use Refresh view to check again.', update_conflict:'The correction target or retry conflicts with existing evidence.', invalid_update:'The update is invalid. Check the quantity and selected requirement.', chat_session_required:'Create a chat session to continue.', message_id_conflict:'This message ID was already used with different text.' };
function notice(text,error=false){ el('status').textContent=text; el('status').classList.toggle('error',error); }
async function api(path,body,session=null){
 const headers={Authorization:'Bearer '+operator}; if(session) headers['X-Auralink-Session-Token']=session.session_tool_token;
 if(body!==undefined) headers['Content-Type']='application/json';
 const response=await fetch(path,{method:body===undefined?'GET':'POST',headers,body:body===undefined?undefined:JSON.stringify(body),cache:'no-store',redirect:'error'});
 const data=await response.json(); if(!response.ok) throw new Error(errors[data.error]||('Request failed: '+(data.error||response.status))); return data;
}
function controls(){
 el('handoff').disabled=busy||!!pending;
 el('start').disabled=busy||!!pending||!selected||!el('thread').value;
 el('resume').disabled=busy||!!pending||!voice||el('thread').value==='__new';
 el('send').disabled=busy||!chat||!!pending; el('message').disabled=busy||!chat||!!pending;
 el('confirm').disabled=busy||!candidate||!!pending; el('refresh').disabled=busy||!selected;
 el('subject').disabled=busy||!!pending; el('thread').disabled=busy||!!pending||!selected;
 el('requirement-form').querySelector('button').disabled=busy||!!pending;
}
async function action(fn){ if(busy)return; busy=true; controls(); try{await fn();}catch(e){notice(e.message,true);}finally{busy=false;controls();} }
function turn(label,text,user=false){const node=document.createElement('div');node.className='turn'+(user?' user':'');const title=document.createElement('span');title.className='turn-label';title.textContent=label;node.append(title,document.createTextNode(text));el('messages').querySelector('.empty')?.remove();el('messages').append(node);el('messages').scrollTop=el('messages').scrollHeight;}
function showContext(c){ envelope=c; el('memory').textContent=c.variables.memory_md;el('hash').textContent='SHA-256 '+c.content_sha256;el('version').textContent='Memory v'+c.variables.memory_version;el('publication').textContent=c.processing.pending_relevant_update?'Refresh pending':'Published';const facts=el('facts');facts.replaceChildren();for(const f of c.variables.current_constraints){const k=document.createElement('dt'),v=document.createElement('dd');k.textContent=f.field;v.textContent=f.value+' · '+f.status;facts.append(k,v);} if(!facts.childElementCount){const k=document.createElement('dt'),v=document.createElement('dd');k.textContent='Requirement';v.textContent='Choose a session thread to view its details.';facts.append(k,v);}}
function clearConversation(){voice=null;chat=null;candidate=null;pending=null;retryTurn=null;retryRequirement=null;el('candidate').hidden=true;el('new-requirement').hidden=true;el('messages').replaceChildren();el('freshness').textContent='';el('message').value='';}
function memoryPath(){return '/v1/users/'+encodeURIComponent(selected.glid)+'/memory?role='+encodeURIComponent(selected.role);}
async function checkpoint(){const current=chat||voice;const c=await api(current?'/v1/sessions/'+current.session.session_id+'/checkpoint':memoryPath());showContext(c);return c;}
async function selectSubject(){clearConversation();selected=el('subject').value===''?null:subjects[Number(el('subject').value)]||null;el('thread').replaceChildren(new Option('Choose a requirement…',''));if(!selected){el('memory').textContent='Choose a user to load memory.';el('facts').replaceChildren();el('hash').textContent='';el('version').textContent='No memory loaded';el('publication').textContent='Awaiting selection';controls();return;}const c=await api(memoryPath());showContext(c);const choices=await api('/v1/users/'+selected.glid+'/threads?role='+selected.role);for(const t of choices.threads)el('thread').append(new Option(t.title+' · '+t.status,t.thread_ref));el('thread').append(new Option('Start a new requirement','__new'));el('source-note').textContent=selected.namespace==='synthetic_demo'?'Synthetic demo data. Safe to use for a rehearsed correction.':'Historical dataset context. Confirm what is still relevant; saved edits become client-reported updates.';notice('Select the requirement you want to continue.');}
async function createVoice(thread){voice=await api('/v1/sessions',{glid:selected.glid,role:selected.role,channel:'voice',active_thread_ref:thread});showContext(voice.context);turn('VOICE-LABELLED SESSION · NO AUDIO',voice.context.variables.next_question);}
async function waitRefresh(){
 if(!pending)return;
 const task=pending;notice('Correction accepted. Waiting for the refreshed memory…');
 for(let i=0;i<35;i++){
  const state=await api('/v1/sessions/'+task.session.session.session_id+'/updates/'+encodeURIComponent(task.key),undefined,task.session);
  if(state.state==='dead'){notice('Refresh failed after retries. Inspect the task status before continuing.',true);return;}
  if(state.state==='done'){
   let c=await checkpoint();if(c.processing.pending_relevant_update){notice('This edit is published, but another update is still refreshing.');return;}
   el('freshness').textContent='Accepted → published '+Number(state.acceptance_to_publication_ms).toFixed(1)+' ms · file ready '+Number(state.acceptance_to_file_ms).toFixed(1)+' ms';
   pending=null;candidate=null;el('candidate').hidden=true;
   if(task.newThread){el('thread').append(new Option('New confirmed requirement',task.newThread));el('thread').value=task.newThread;el('new-requirement').hidden=true;retryRequirement=null;await createVoice(task.newThread);c=voice.context;}
   turn('MEMORY UPDATED','The confirmed change is in memory v'+c.variables.memory_version+'. '+c.variables.next_question);notice('Memory refreshed. Continue with the saved details.');return;
  }
  await new Promise(resolve=>setTimeout(resolve,400));
 }
 notice('Refresh is still pending. Use Refresh view to check again.');
}
el('login-form').addEventListener('submit',event=>{event.preventDefault();action(async()=>{operator=el('operator-token').value.trim();el('operator-token').value='';try{subjects=(await api('/v1/demo/subjects')).subjects;}catch(e){operator='';throw e;}el('subject').replaceChildren(new Option('Choose a user…',''));subjects.forEach((s,i)=>el('subject').append(new Option((s.namespace==='synthetic_demo'?'Synthetic · ':'Historical · ')+s.role+' '+s.glid,String(i))));el('login').hidden=true;el('workspace').hidden=false;el('logout').hidden=false;notice('Workspace unlocked.');});});
el('logout').addEventListener('click',()=>{operator='';clearConversation();location.reload();});
el('handoff-form').addEventListener('submit',event=>{event.preventDefault();action(async()=>{
 const id=el('voice-session-id').value.trim();if(!/^[0-9a-f]{64}$/.test(id))throw new Error('Enter a valid voice session ID.');
 const resumed=await api('/v1/sessions',{channel:'chat',resumed_from_id:id});
 const scope=resumed.session.scope;
 const index=subjects.findIndex(s=>s.glid===scope.glid&&s.role===scope.role&&s.namespace===scope.namespace&&s.tenant_id===scope.tenant_id);
 if(index<0)throw new Error('The resumed user is unavailable in this workspace.');
 el('subject').value=String(index);await selectSubject();
 const thread=resumed.session.active_thread_ref;
 if(thread&&!Array.from(el('thread').options).some(o=>o.value===thread))el('thread').append(new Option('Resumed requirement',thread));
 el('thread').value=thread;chat=resumed;showContext(chat.context);
 turn('VOICE → CHAT','Resumed the actual Sarvam session with its original user, role and selected requirement.');
 const reply=await api('/v1/sessions/'+chat.session.session_id+'/chat',{message_id:crypto.randomUUID(),message:'Continue from the saved requirement'},chat);
 turn('CHAT · MEMORY v'+reply.memory_version,reply.reply);notice('Voice session resumed in chat. The preparation panel uses the same published memory.');
});});
el('subject').addEventListener('change',()=>action(selectSubject));
el('thread').addEventListener('change',()=>action(async()=>{clearConversation();if(selected)showContext(await api(memoryPath()));}));
el('start').addEventListener('click',()=>action(async()=>{clearConversation();const thread=el('thread').value;await createVoice(thread==='__new'?'':thread);el('new-requirement').hidden=thread!=='__new';notice(thread==='__new'?'Confirm the new requirement before continuing.':'Session prepared. Continue in chat with the same requirement.');}));
el('resume').addEventListener('click',()=>action(async()=>{chat=await api('/v1/sessions',{channel:'chat',resumed_from_id:voice.session.session_id});showContext(chat.context);const reply=await api('/v1/sessions/'+chat.session.session_id+'/chat',{message_id:crypto.randomUUID(),message:'Continue from the saved requirement'},chat);turn('CHAT · MEMORY v'+reply.memory_version,reply.reply);notice('Chat resumed with the same user, role and requirement.');}));
el('chat-form').addEventListener('submit',event=>{event.preventDefault();action(async()=>{const message=el('message').value.trim();if(!message)return;if(!retryTurn||retryTurn.message!==message)retryTurn={message_id:crypto.randomUUID(),message};const reply=await api('/v1/sessions/'+chat.session.session_id+'/chat',retryTurn,chat);retryTurn=null;turn('YOU',message,true);turn('CHAT · MEMORY v'+reply.memory_version,reply.reply);el('message').value='';candidate=reply.candidate||null;el('candidate').hidden=!candidate;if(candidate)el('candidate-text').textContent=Object.entries(candidate.fields).map(([k,v])=>k+' = '+v).join(', ')+' · Evidence: “'+candidate.evidence+'”';notice(candidate?'Review the proposed correction. It is not saved yet.':'Reply uses the selected requirement’s saved context.');});});
el('confirm').addEventListener('click',()=>action(async()=>{const edit=candidate;await api('/v1/sessions/'+chat.session.session_id+'/updates',edit,chat);pending={key:edit.idempotency_key,session:chat};await waitRefresh();}));
el('cancel-edit').addEventListener('click',()=>{candidate=null;el('candidate').hidden=true;notice('Edit discarded. Canonical memory is unchanged.');controls();});
el('refresh').addEventListener('click',()=>action(async()=>{if(pending)await waitRefresh();else{await checkpoint();notice('Loaded the latest canonical memory.');}}));
el('requirement-form').addEventListener('submit',event=>{event.preventDefault();action(async()=>{const fields={product:el('product').value.trim(),quantity:el('quantity').value.trim(),unit:el('unit').value.trim(),delivery_city:el('city').value.trim()};if(!retryRequirement||JSON.stringify(retryRequirement.fields)!==JSON.stringify(fields))retryRequirement={idempotency_key:crypto.randomUUID(),kind:'new_requirement',fields,evidence:'Confirmed form: '+Object.entries(fields).map(([k,v])=>k+' = '+v).join('; ')};const receipt=await api('/v1/sessions/'+voice.session.session_id+'/updates',retryRequirement,voice);pending={key:retryRequirement.idempotency_key,session:voice,newThread:receipt.thread_ref};await waitRefresh();});});
