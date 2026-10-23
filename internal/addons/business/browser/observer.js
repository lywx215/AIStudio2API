() => {
  if (location.origin !== 'https://business.gemini.google' || window.__businessObserve) return;
  const queue = [];
  // Only minimal selectors leave the page. This observer never initiates fetch,
  // XHR, login, generation or a model probe.
  Object.defineProperty(window, '__businessObserve', {value: () => queue.splice(0, 64)});
  const address = raw => {
    try { const u = new URL(raw, location.href);
      if (u.protocol !== 'https:' || !['business.gemini.google', 'biz-discoveryengine.googleapis.com'].includes(u.host)) return null;
      return u.origin + u.pathname;
    } catch { return null; }
  };
  const scope = () => {
    const page = new URL(location.href), segments = page.pathname.split('/');
    const position = segments.indexOf('cid');
    return {config_id: position >= 0 ? (segments[position + 1] || '') : '', csesidx: page.searchParams.get('csesidx') || ''};
  };
  const record = (source, url, obj, bound) => {
    if (!url || !obj || typeof obj !== 'object' || Array.isArray(obj) || queue.length >= 64) return;
    let payload;
    const configId = typeof obj.configId === 'string' && obj.configId.length <= 256 ? obj.configId : undefined;
    if (source === 'model_config') {
      const models = (obj.widgetConfig || obj)?.uiSettings?.modelConfigInfo?.resolvedModels;
      if (!Array.isArray(models) || models.length > 128) return;
      const resolvedModels = models.filter(m => m && typeof m.modelId === 'string' && m.modelId.length <= 256).map(m => ({modelId:m.modelId, displayName:typeof m.displayName === 'string' ? m.displayName.slice(0,256) : ''}));
      if (!resolvedModels.length) return;
      payload = {configId, uiSettings:{modelConfigInfo:{resolvedModels}}};
    } else {
      if (!Object.hasOwn(obj, 'streamAssistRequest') || !obj.streamAssistRequest || typeof obj.streamAssistRequest !== 'object') return;
      const a = obj.streamAssistRequest, out = {};
      if (Object.hasOwn(a, 'assistGenerationConfig')) {
        if (!a.assistGenerationConfig || typeof a.assistGenerationConfig !== 'object') return;
        out.assistGenerationConfig = {};
        if (Object.hasOwn(a.assistGenerationConfig, 'modelId')) {
          if (typeof a.assistGenerationConfig.modelId !== 'string' || a.assistGenerationConfig.modelId.length > 256) return;
          out.assistGenerationConfig.modelId = a.assistGenerationConfig.modelId;
        }
      }
      payload = {configId,streamAssistRequest:out};
    }
    queue.push({source,url,payload,...bound});
  };
  const request = (url, body, bound) => { if (typeof body !== 'string' || body.length > 262144) return; try {record('user_request',url,JSON.parse(body),bound);} catch {} };
  const originalFetch = window.fetch;
  let reading = 0;
  window.fetch = function(resource, init) {
    const url = address(typeof resource === 'string' || resource instanceof URL ? String(resource) : resource?.url);
    const bound = scope();
    if (url) request(url,init?.body,bound);
    const promise = Reflect.apply(originalFetch,this,arguments);
    if (url && !url.includes('streamAssist') && reading < 2 && queue.length < 64) { reading++; promise.then(async response => {
      if (!response.ok || !response.headers.get('content-type')?.includes('json')) return;
      const reader = response.clone().body?.getReader(); if (!reader) return;
      const chunks=[];let size=0;
      try { for (;;) { const r=await reader.read();if(r.done)break;size+=r.value.byteLength;if(size>262144){void reader.cancel();return;}chunks.push(r.value); }
        const data=new Uint8Array(size);let p=0;for(const chunk of chunks){data.set(chunk,p);p+=chunk.length;}
        record('model_config',url,JSON.parse(new TextDecoder().decode(data)),bound);
      } catch {} finally {reader.releaseLock();}
    }).catch(()=>{}).finally(()=>{reading--;}); }
    return promise;
  };
  const open=XMLHttpRequest.prototype.open, send=XMLHttpRequest.prototype.send;
  const urls=new WeakMap();
  XMLHttpRequest.prototype.open=function(method,url){urls.set(this,address(url));return Reflect.apply(open,this,arguments);};
  XMLHttpRequest.prototype.send=function(body){
    const url=urls.get(this),bound=scope();if(url){request(url,body,bound);this.addEventListener('load',()=>{try{if(this.status!==200||url.includes('streamAssist'))return;
      if(this.responseType==='json'){record('model_config',url,this.response,bound);}
      else if((this.responseType===''||this.responseType==='text')&&this.responseText.length<=262144)record('model_config',url,JSON.parse(this.responseText),bound);
    }catch{}},{once:true});}
    return Reflect.apply(send,this,arguments);
  };
}
