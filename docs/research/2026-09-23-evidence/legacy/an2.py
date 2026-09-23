import pickle, pandas as pd, numpy as np
from scipy.stats import spearmanr
from load import dedupe
from an1 import ci, week
rows=pickle.load(open('rows.pkl','rb'))
out=[]
def P(*a):
    s=' '.join(str(x) for x in a); print(s); out.append(s)
def summ(d,col):
    return f"n={len(d)} mean={d[col].mean():+.2f} hit={(d[col]>0).mean():.0%} CI={ci(d[col],d.wk)}"
for H in (10,15):
    P(f'\n######## H={H}')
    # shortlist arm at scout bias (dedup) split by shipped
    sl=[r for r in rows if r['bias'] and r.get(f'ex{H}') is not None]
    # 2. shipped vs not: evaluate at scout bias, restrict to runs that shipped >=1
    shipruns={r['run'] for r in rows if r['shipped']}
    s2=dedupe([r for r in sl if r['run'] in shipruns])
    d=pd.DataFrame(s2); d['wk']=[week(r) for r in s2]
    for era in ('all','old','new'):
        e=d if era=='all' else d[d.era==era]
        P(f'[2] {era} shortlist@bias shipped: {summ(e[e.shipped],f"ex{H}")} | not shipped: {summ(e[~e.shipped],f"ex{H}")}')
    # shipped ideas at shipped direction (dedup)
    sh=[dict(r,direction=r['ship_dir']) for r in rows if r['shipped'] and r.get(f'sx{H}') is not None]
    sh=dedupe(sh); s=pd.DataFrame(sh); s['wk']=[week(r) for r in sh]
    for era in ('all','old','new'):
        e=s if era=='all' else s[s.era==era]
        P(f'[2] {era} shipped@ship dir: {summ(e,f"sx{H}")}')
    flip=s[s.bias!=s.ship_dir.map({'BUY':1,'SELL':-1})]
    P(f'[2] shipped against scout bias: n={len(flip)} mean={flip[f"sx{H}"].mean():+.2f}')
    s['adj']=s.conf-s.base_conf
    m=s.base_conf.notna()&(s.base_conf>0)
    if m.sum()>5:
        for c in ('conf','base_conf','adj'):
            rho,p=spearmanr(s.loc[m,c],s.loc[m,f'sx{H}']); P(f'[2] shipped IC {c} vs sx{H}: rho={rho:+.3f} p={p:.2f} n={m.sum()}')
        e=s[m]
        P('[2] adj buckets:', e.groupby(pd.cut(e.adj,[-99,-3,-0.5,0.5,3,99]),observed=True)[f'sx{H}'].agg(['count','mean']).round(2).to_dict('index'))
        P('[2] adj describe', e.adj.describe().round(2).to_dict())
    rho,p=spearmanr(s.conf,s[f'sx{H}']); P(f'[2] shipped IC confidence (all eras) rho={rho:+.3f} p={p:.2f} n={len(s)}')
    # rank within run
    s['rank']=[r['idea'].get('rank') for r in sh]
    P('[2] by rank:', s.groupby('rank')[f'sx{H}'].agg(['count','mean']).round(2).to_dict('index'))
    # 3. breakdowns
    dsl=dedupe(sl); D=pd.DataFrame(dsl); D['wk']=[week(r) for r in dsl]; D['dir']=D.bias.map({1:'long',-1:'short'})
    s['dir']=s.ship_dir.map({'BUY':'long','SELL':'short'})
    for g in ('setup','dir','region','sector','era'):
        a=D.groupby(g)[f'ex{H}'].agg(['count','mean']).round(2)
        b=s.groupby(g)[f'sx{H}'].agg(['count','mean']).round(2)
        t=a.join(b,how='outer',lsuffix='_short',rsuffix='_ship')
        P(f'[3] by {g}:\n'+t.to_string())
    # 4. bias vs composite sign
    c=D[D.comp.notna()].copy(); c['agree']=np.sign(c.comp)==c.bias
    P('[4] scout bias vs composite sign:\n'+c.groupby(['agree','setup'])[f'ex{H}'].agg(['count','mean']).round(2).to_string())
    P('[4] totals:', c.groupby('agree')[f'ex{H}'].agg(['count','mean']).round(2).to_dict('index'))
    # nominations / contested
    P('[3] nominations:', D.groupby(D.nominations.clip(upper=2))[f'ex{H}'].agg(['count','mean']).round(2).to_dict('index'))
open('an2.txt','w').write('\n'.join(out))
