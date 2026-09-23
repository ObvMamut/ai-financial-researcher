import pickle, pandas as pd, numpy as np
from scipy.stats import spearmanr
from load import dedupe
rows=pickle.load(open('rows.pkl','rb'))
rng=np.random.default_rng(0)
def ci(x, groups=None, B=2000):
    x=np.asarray(x,float)
    if len(x)<3: return (np.nan,np.nan)
    if groups is None:
        m=[rng.choice(x,len(x)).mean() for _ in range(B)]
    else:
        g=pd.Series(x).groupby(np.asarray(groups)); arrs=[v.values for _,v in g]
        m=[]
        for _ in range(B):
            pick=rng.integers(0,len(arrs),len(arrs)); m.append(np.concatenate([arrs[i] for i in pick]).mean())
    return tuple(np.round(np.percentile(m,[2.5,97.5]),2))
def week(r): return r['at'].strftime('%G-W%V')
DOMS=['quant','news','fundamentals','sentiment','macro']
out=[]
def P(*a):
    s=' '.join(str(x) for x in a); print(s); out.append(s)
for H in (10,15):
    sl=[r for r in rows if r['bias'] and r.get(f'ex{H}') is not None]
    sl=dedupe(sl)
    df=pd.DataFrame(sl); df['wk']=[week(r) for r in sl]; y=df[f'ex{H}']
    P(f'\n## H={H} shortlist (dedup, non-neutral bias) n={len(df)} mean={y.mean():.2f} CI(wk-cluster)={ci(y,df.wk)}')
    P('domain | era | n scored | IC (spearman aligned score vs excess) | p | agree n/mean | disagree n/mean | neutral(0) n/mean | missing n/mean')
    for era in ('all','old','new'):
        d0=df if era=='all' else df[df.era==era]
        for dm in DOMS:
            col='d_'+dm
            if col not in d0: continue
            al=d0[col]*d0['bias']
            m=al.notna()
            if m.sum()<5: continue
            rho,p=spearmanr(al[m],d0.loc[m,f'ex{H}'])
            ag=d0.loc[al>0,f'ex{H}']; dg=d0.loc[al<0,f'ex{H}']; nz=d0.loc[al==0,f'ex{H}']; ms=d0.loc[~m,f'ex{H}']
            P(f'{dm} | {era} | {m.sum()} | {rho:+.3f} | {p:.2f} | {len(ag)}/{ag.mean():+.2f} | {len(dg)}/{dg.mean():+.2f} | {len(nz)}/{nz.mean():+.2f} | {len(ms)}/{ms.mean():+.2f}')
        for col,lab in (('base_al','base(aligned, run weights)'),):
            m=d0[col].notna()
            rho,p=spearmanr(d0.loc[m,col],d0.loc[m,f'ex{H}'])
            P(f'{lab} | {era} | {m.sum()} | {rho:+.3f} | {p:.2f}')
        m=d0['comp'].notna()
        if m.sum()>5:
            rho,p=spearmanr(d0.loc[m,'comp']*d0.loc[m,'bias'],d0.loc[m,f'ex{H}'])
            P(f'composite(aligned) | {era} | {m.sum()} | {rho:+.3f} | {p:.2f}')
    # quintile of base
    P('base_al terciles (all eras):', df.groupby(pd.qcut(df.base_al,3,duplicates="drop"),observed=True)[f'ex{H}'].agg(['count','mean']).round(2).to_dict('index'))
    # partial: domain IC within new era controlling for composite? residualize
    d=df[(df.era=='new')&df.comp.notna()].copy()
    d['comp_al']=d.comp*d.bias
    for dm in DOMS:
        al=(d['d_'+dm]*d.bias).fillna(0)
        X=np.column_stack([np.ones(len(d)),d.comp_al,al]); b=np.linalg.lstsq(X,d[f'ex{H}'],rcond=None)[0]
        res=d[f'ex{H}']-X@b; se=np.sqrt((res**2).sum()/(len(d)-3)*np.linalg.inv(X.T@X)[2,2])
        P(f'OLS new-era ex{H} ~ comp_al + {dm}(missing=0): coef {b[2]:+.3f}%/pt t={b[2]/se:+.2f} n={len(d)}')
open(f'an1.txt','w').write('\n'.join(out))
