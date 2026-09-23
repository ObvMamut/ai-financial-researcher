import pickle, pandas as pd, numpy as np, math
from load import dedupe, bars, runs
rows=pickle.load(open('rows.pkl','rb'))
out=[]
def P(*a):
    s=' '.join(str(x) for x in a); print(s); out.append(s)
recs=[]
for r in rows:
    if not r['shipped']: continue
    i=r['idea']; t=r['ticker']; b=bars(t); gd=r['at'].strftime('%Y-%m-%d')
    q=runs[r['run']]['quant'].get(t,{})
    sig=q.get('sigma_daily')
    prior=[x['c'] for x in b if x['date']<gd][-61:]
    sig_y=np.std(np.diff(np.log(prior)),ddof=1) if len(prior)>20 else None
    s=sig or sig_y
    H=i.get('timeframe_days') or 10
    pg=i.get('price_at_generation') or (prior[-1] if prior else None)
    e=i.get('entry') or pg; st=i.get('stop'); tg=i.get('target'); buy=i['direction']=='BUY'
    rec=dict(run=r['run'],ticker=t,direction=i['direction'],at=r['at'],era=r['era'],H=H,sig=s,sig_src='quant' if sig else 'yahoo',conf=i.get('confidence'))
    if s and st and tg and e and pg:
        u=s*math.sqrt(H)
        rec.update(stop_u=abs(e-st)/e/u, tgt_u=abs(tg-e)/e/u, rr=abs(tg-e)/abs(e-st), entry_off=((pg-e)/pg*(1 if buy else -1))/(s*math.sqrt(5)))
    # replay
    after=[x for x in b if x['date']>gd]
    fill=None
    for k,x in enumerate(after[:3]):
        if buy:
            if x['o']<=e: fill=(k,x['o']);break
            if x['l']<=e<=x['h']: fill=(k,e);break
        else:
            if x['o']>=e: fill=(k,x['o']);break
            if x['l']<=e<=x['h']: fill=(k,e);break
    if fill is None:
        rec['outcome']='open' if len(after)<3 else 'unfilled'
    else:
        tb=after[fill[0]:]; oc=None
        for k,x in enumerate(tb[:H]):
            hs=st and ((buy and x['l']<=st) or (not buy and x['h']>=st))
            ht=tg and ((buy and x['h']>=tg) or (not buy and x['l']<=tg))
            if hs:
                px=st
                if buy and x['o']<st: px=x['o']
                if not buy and x['o']>st: px=x['o']
                oc=('stop',px,k+1);break
            if ht:
                px=tg
                if buy and x['o']>tg: px=x['o']
                if not buy and x['o']<tg: px=x['o']
                oc=('target',px,k+1);break
        if oc is None:
            oc=('expired',tb[H-1]['c'],H) if len(tb)>=H else ('open',tb[-1]['c'],len(tb))
        rec['outcome']=oc[0]; rec['bars']=oc[2]
        pnl=(oc[1]/fill[1]-1)*(1 if buy else -1)
        rec['pnl']=100*pnl; rec['R']=pnl*fill[1]/abs(fill[1]-st) if st else None
    recs.append(rec)
D=pd.DataFrame(recs)
Dd=pd.DataFrame(dedupe([dict(x) for x in recs]))
for lab,d in (('all shipped (not dedup)',D),('dedup',Dd)):
    P(f'\n## {lab} n={len(d)}')
    P('outcomes:', d.outcome.value_counts().to_dict())
    closed=d[d.outcome.isin(['stop','target','expired'])]
    filled=d[d.outcome!='unfilled']; 
    P(f"fill rate (excl open-unresolved): {(d.outcome!='unfilled').sum()}/{len(d[d.outcome!='open'])+ (d.outcome=='open').sum()}")
    P('closed by outcome mean pnl% / R:', closed.groupby('outcome')[['pnl','R']].agg(['count','mean']).round(2).to_dict())
    P(f"closed n={len(closed)} mean pnl={closed.pnl.mean():+.2f}% mean R={closed.R.mean():+.2f} win={(closed.pnl>0).mean():.0%}")
    m=d.stop_u.notna()
    P('stop dist (σ·√H):', d.loc[m,'stop_u'].describe().round(2).to_dict())
    P('target dist (σ·√H):', d.loc[m,'tgt_u'].describe().round(2).to_dict())
    P('R:R:', d.loc[m,'rr'].describe().round(2).to_dict())
    P('entry offset patient-side (σ·√5):', d.loc[m,'entry_off'].describe().round(2).to_dict())
    P('timeframe H:', d.H.describe().round(1).to_dict())
    P('by era outcomes:', d.groupby('era').outcome.value_counts().unstack(fill_value=0).to_dict('index'))
    P('stop_u by era:', d.groupby('era').stop_u.median().round(2).to_dict(), 'tgt_u by era:', d.groupby('era').tgt_u.median().round(2).to_dict())
    # entry offset vs unfilled
    P('unfilled vs entry_off (median):', d.groupby(d.outcome=='unfilled').entry_off.median().round(2).to_dict())
D.to_csv('shipped_trades.csv',index=False)
open('an5.txt','w').write('\n'.join(out))
