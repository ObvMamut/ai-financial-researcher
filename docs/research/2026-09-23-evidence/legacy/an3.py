import pickle, pandas as pd, numpy as np, csv, glob, os
from load import dedupe
from an1 import ci, week
rows=pickle.load(open('rows.pkl','rb'))
sec={}
for f in glob.glob('/home/mamut/all/programming/claude-financial-researcher/internal/universe/data/*.csv'):
    for r in csv.DictReader(l for l in open(f) if not l.startswith('#')): sec.setdefault(r['ticker'],r['sector'])
for r in rows:
    if r['sector']=='?': r['sector']=sec.get(r['ticker'],'?')
out=[]
def P(*a):
    s=' '.join(str(x) for x in a); print(s); out.append(s)
for H in (10,15):
    P(f'\n######## H={H}')
    sl=dedupe([r for r in rows if r['bias'] and r.get(f'ex{H}') is not None])
    D=pd.DataFrame(sl); D['dir']=D.bias.map({1:'long',-1:'short'})
    sh=dedupe([dict(r,direction=r['ship_dir']) for r in rows if r['shipped'] and r.get(f'sx{H}') is not None])
    S=pd.DataFrame(sh); S['dir']=S.ship_dir.map({'BUY':'long','SELL':'short'})
    t=D.groupby('sector')[f'ex{H}'].agg(['count','mean']).round(2).join(S.groupby('sector')[f'sx{H}'].agg(['count','mean']).round(2),how='outer',lsuffix='_short',rsuffix='_ship')
    P('[3] sector (all eras):\n'+t.to_string())
    for g in ('dir','region'):
        for era in ('old','new'):
            a=D[D.era==era].groupby(g)[f'ex{H}'].agg(['count','mean']).round(2); b=S[S.era==era].groupby(g)[f'sx{H}'].agg(['count','mean']).round(2)
            P(f'[3] {g} era={era}:\n'+a.join(b,how='outer',lsuffix='_short',rsuffix='_ship').to_string())
    # setup: only prescreen runs, dedup within them
    ps=[r for r in rows if r['comp'] is not None]
    sl2=dedupe([r for r in ps if r['bias'] and r.get(f'ex{H}') is not None]); D2=pd.DataFrame(sl2)
    sh2=dedupe([dict(r,direction=r['ship_dir']) for r in ps if r['shipped'] and r.get(f'sx{H}') is not None]); S2=pd.DataFrame(sh2)
    P(f'[3] setup (prescreen runs 08-31..09-06, dedup):\n'+D2.groupby('setup')[f'ex{H}'].agg(['count','mean']).round(2).join(S2.groupby('setup')[f'sx{H}'].agg(['count','mean']).round(2),how='outer',lsuffix='_short',rsuffix='_ship').to_string())
    # undeduped for more power
    U=pd.DataFrame([r for r in ps if r['bias'] and r.get(f'ex{H}') is not None]); U['agree']=np.sign(U.comp)==U.bias
    US=pd.DataFrame([r for r in ps if r['shipped'] and r.get(f'sx{H}') is not None])
    P(f'[3] setup NOT deduped (every run-name, heavy overlap):\n'+U.groupby('setup')[f'ex{H}'].agg(['count','mean']).round(2).join(US.groupby('setup')[f'sx{H}'].agg(['count','mean']).round(2),how='outer',lsuffix='_short',rsuffix='_ship').to_string())
    P('[4] bias vs composite sign NOT deduped:\n'+U.groupby(['agree','setup'])[f'ex{H}'].agg(['count','mean']).round(2).to_string())
    U['dir']=U.bias.map({1:'long',-1:'short'})
    P('[4] direction x setup NOT deduped:\n'+U.groupby(['setup','dir'])[f'ex{H}'].agg(['count','mean']).round(2).to_string())
open('an3.txt','w').write('\n'.join(out))
