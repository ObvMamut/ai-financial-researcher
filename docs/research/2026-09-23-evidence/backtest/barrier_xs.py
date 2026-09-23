import numpy as np, pandas as pd
P = pd.read_pickle('panel.pkl')
cnt = P.groupby(['date','index']).size().unstack(); good=(cnt>=0.7*cnt.max()).all(axis=1); P=P[P.date.isin(good[good].index)]
T = pd.concat([g[g.score.notna()&(g.score!=0)].loc[lambda x: x.score.abs().sort_values(ascending=False).index[:5]] for _, g in P.groupby(['date','index'])])
T['dir']=np.sign(T.score); T['dx']=T.dir*T.xs15
mid=np.sort(T.date.unique())[len(T.date.unique())//2]
wk=T.groupby('date').dx.mean().dropna()
out=[f'Top-5 picks, 15-session benchmark-EXCESS directional return (close d -> close d+15, gross): mean {100*T.dx.mean():.3f}%',
     f'  by side: long {100*T[T.dir>0].dx.mean():.3f}%  short {100*T[T.dir<0].dx.mean():.3f}%',
     f'  H1 {100*T[T.date<mid].dx.mean():.3f}%  H2 {100*T[T.date>=mid].dx.mean():.3f}%',
     f'  by index: '+str((100*T.groupby("index").dx.mean()).round(3).to_dict()),
     f'  weekly-portfolio mean {100*wk.mean():.3f}%, non-overlap (every 3rd wk) t = {np.mean([wk.values[o::3].mean()/(wk.values[o::3].std(ddof=1)/np.sqrt(len(wk.values[o::3]))) for o in range(3)]):.2f}']
print('\n'.join(out)); open('report_barrier.txt','a').write('\n\n'+'\n'.join(out))
