import json, re, os, glob, math
from datetime import datetime, timedelta
ROOT='/home/mamut/all/programming/claude-financial-researcher'
S='/tmp/claude-1000/-home-mamut-all-programming-claude-financial-researcher/003eeaf0-aff6-433e-b777-9f80114dd220/scratchpad/legacy'
DOMS=['technicals','news','fundamentals','quant','sentiment','macro']
def lastjson(txt):
    blocks=re.findall(r"```json\s*(.*?)```", txt, re.S)
    for b in reversed(blocks):
        try: return json.loads(b)
        except Exception: pass
    # bare
    i=txt.find('{')
    try: return json.loads(txt[i:txt.rfind('}')+1])
    except Exception: return None
def sgn(b): return {'bullish':1,'bearish':-1}.get((b or '').lower(),0)
runs={}
for d in sorted(glob.glob(ROOT+'/runs/*/')):
    name=os.path.basename(d.rstrip('/'))
    if not os.path.exists(d+'news.md') or not os.path.exists(d+'metadata.json'): continue
    meta=json.load(open(d+'metadata.json'))
    if meta.get('mode')!='independent': continue
    ideas=json.load(open(d+'ideas.json')) if os.path.exists(d+'ideas.json') else {}
    if ideas.get('research_mode')=='thesis': continue
    dom={}
    for dm in DOMS:
        p=d+dm+'.md'
        if not os.path.exists(p): continue
        j=lastjson(open(p).read())
        if not j: print('parse fail',name,dm); continue
        sc={}
        for s in j.get('scores',[]) or []:
            sc[s['ticker']]=sgn(s.get('bias'))*int(s.get('strength') or 0)
        dom[dm]=(sc,set(j.get('missing') or []))
    q={}
    if os.path.exists(d+'quant.json'):
        q=json.load(open(d+'quant.json')).get('by_ticker',{})
    ps={}
    if os.path.exists(d+'prescreen.json'):
        for r in json.load(open(d+'prescreen.json')).get('rows',[]): ps[r['ticker']]=r
    runs[name]=dict(meta=meta,ideas=ideas.get('ideas') or [],dom=dom,quant=q,ps=ps,weights=meta.get('weights'))
def ctrl(h):
    c=json.load(open(f'{S}/control{h}.json'))
    return {a['name']:a['entries'] for a in c['arms']}
def dedupe(rows,key=lambda r:(r['ticker'].upper(),r['direction'])):
    rows=sorted(rows,key=lambda r:r['at'])
    last={};out=[]
    for r in rows:
        k=key(r)
        if k in last and r['at']-last[k]<timedelta(days=7): continue
        last[k]=r['at'];out.append(r)
    return out
def pat(s): return datetime.fromisoformat(s.replace('Z','+00:00'))
from datetime import timezone
_px={}
def bars(t):
    if t in _px: return _px[t]
    p=S+'/px/'+t.replace('^','_')+'.json'
    out=[]
    if os.path.exists(p):
        r=json.load(open(p))['chart']['result'][0]
        off=r['meta'].get('gmtoffset',0)
        q=r['indicators']['quote'][0]
        for i,ts in enumerate(r.get('timestamp',[])):
            c=q['close'][i]
            if c is None: continue
            d=datetime.fromtimestamp(ts+off,timezone.utc).strftime('%Y-%m-%d')
            out.append(dict(date=d,o=q['open'][i],h=q['high'][i],l=q['low'][i],c=c))
    _px[t]=out; return out
BENCH={'sp500':'^GSPC','nq100':'^NDX','eu50':'^STOXX50E','asia100':'^N225'}
MB={'T':'^N225','HK':'^HSI','NS':'^NSEI','AX':'^AXJO','TW':'^TWII','KS':'^KS11','SI':'^STI','BK':'^SET.BK'}
def bench(idx,t):
    if idx=='asia100' and '.' in t:
        s=t.rsplit('.',1)[1].upper()
        if s in MB: return MB[s]
    return BENCH.get(idx,'^GSPC')
def closeutc(t):
    s=t.rsplit('.',1)[1].upper() if '.' in t else ''
    if s in ('T','HK','TW','KS','SI','BK','AX'): return 9
    if s=='NS': return 10.5
    if s: return 16.5
    return 20.5
def region(idx,t):
    if idx=='asia100': return 'asia'
    if idx=='eu50': return 'eu'
    return 'us'
def ret_between(b,d0,d1):
    c0=[x['c'] for x in b if x['date']<=d0]; c1=[x['c'] for x in b if x['date']<=d1]
    if not c0 or not c1: return None
    return c1[-1]/c0[-1]-1
def outcome(t,idx,direction,at,H,anchor=None):
    """call excess over H sessions after gen date, direction-aligned (%)."""
    b=bars(t)
    if not b: return None
    gd=at.strftime('%Y-%m-%d'); hr=at.hour+at.minute/60
    prior=[x for x in b if x['date']<gd or (x['date']==gd and hr>=closeutc(t))]
    if not prior: return None
    ad=prior[-1]['date']
    if anchor is None: anchor=prior[-1]['c']
    after=[x for x in b if x['date']>gd]
    if len(after)<H: return None
    end=after[H-1]
    r=end['c']/anchor-1
    sg=1 if direction=='BUY' else -1
    br=ret_between(bars(bench(idx,t)),ad,end['date'])
    if br is None: return None
    return 100*sg*(r-br)
