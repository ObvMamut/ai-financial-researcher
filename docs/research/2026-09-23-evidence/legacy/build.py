from load import *
import pickle
C={H:ctrl(H) for H in (10,15)}
cmap={}
for H in (10,15):
    for arm in ('shortlist','shipped'):
        for e in C[H][arm]:
            if 'call_excess_pct' in e: cmap[(H,arm,e['run'],e['ticker'],e['direction'])]=e['call_excess_pct']
rows=[]
DM={'technicals':'quant'}
for rn,r in runs.items():
    at=pat(r['meta']['generated_at']) if r['meta'].get('generated_at') else datetime.strptime(rn,'%Y-%m-%dT%H-%M-%S').replace(tzinfo=timezone.utc)
    ideas={i['ticker']:i for i in r['ideas']}
    w={k.lower():v for k,v in (r['weights'] or {}).items()}
    w={DM.get(k,k):v for k,v in w.items()}
    for c in r['meta']['shortlist']:
        t=c['ticker']; b=sgn(c.get('bias'))
        idx=c.get('index') or (ideas.get(t,{}).get('index')) or 'sp500'
        row=dict(run=rn,at=at,ticker=t,index=idx,region=region(idx,t),sector=c.get('sector') or '?',setup=c.get('setup') or '?',
                 bias=b,nominations=c.get('nominations',1),contested=bool(c.get('contested')),era='new' if rn>='2026-08-28' else 'old')
        ps=r['ps'].get(t); row['comp']=ps['score'] if ps else None
        for dm,(sc,miss) in r['dom'].items():
            d=DM.get(dm,dm)
            row['d_'+d]=sc.get(t)  # None = missing
        # base (aligned) with run weights
        if b:
            s=0
            for d,wt in w.items():
                v=row.get('d_'+d)
                if v is not None: s+=wt*b*v/10
            row['base_al']=s
        row['direction']='BUY' if b>0 else ('SELL' if b<0 else None)
        i=ideas.get(t)
        row['shipped']=i is not None
        if i:
            row['ship_dir']=i['direction']; row['conf']=i.get('confidence'); row['base_conf']=i.get('base_confidence')
            row['ds']=i.get('domain_scores'); row['idea']=i
        for H in (10,15):
            if b:
                v=cmap.get((H,'shortlist',rn,t,row['direction']))
                row[f'src{H}']='ctrl' if v is not None else 'own'
                if v is None: v=outcome(t,idx,row['direction'],at,H)
                row[f'ex{H}']=v
            if i:
                v=cmap.get((H,'shipped',rn,t,i['direction']))
                if v is None: v=outcome(t,idx,i['direction'],at,H,i.get('price_at_generation'))
                row[f'sx{H}']=v
        rows.append(row)
    # shipped ideas not in shortlist
    sl={c['ticker'] for c in r['meta']['shortlist']}
    for t,i in ideas.items():
        if t not in sl: print('shipped off-shortlist',rn,t)
pickle.dump(rows,open('rows.pkl','wb'))
print(len(rows), sum(r['shipped'] for r in rows), sum(1 for r in rows if r.get('ex10') is not None), sum(1 for r in rows if r.get('ex15') is not None))
