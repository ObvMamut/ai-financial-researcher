import json, numpy as np, pandas as pd, build
d = json.load(open('/home/mamut/all/programming/claude-financial-researcher/runs/2026-09-23T17-02-22/prescreen.json'))
R = pd.DataFrame(d['rows']); R = R[R.score.notna() & (R.get('excluded', pd.Series(['']*len(R))).fillna('') == '')]
out = []
for _, r in R.iterrows():
    try: df = build.load(r.ticker)
    except Exception: continue
    b = build.load(build.bench_for(r['index'], r.ticker))
    df = df[df.index <= pd.Timestamp(r.as_of)]
    s = build.signals(df, b).iloc[-1]
    out.append(dict(index=r['index'], ticker=r.ticker, go_mom=r.mom_12_1, py_mom=s.mom12_1, go_r63=r.ret_63d, py_r63=s.ret63,
                    go_strz=r.str_z, py_strz=s.strz, go_st21=r.stretch_21, py_st21=s.stretch21, go_score=r.score,
                    mom12_1=s.mom12_1, ret63=s.ret63, strz=s.strz, stretch21=s.stretch21))
O = pd.DataFrame(out)
O['py_score'] = np.nan
for idx, g in O.groupby('index'):
    O.loc[g.index, 'py_score'] = build.composite(g)[0]
for a in ['mom', 'r63', 'strz', 'st21', 'score']:
    print(a, 'corr %.3f  medAbsDiff %.4f' % (O['go_'+a].corr(O['py_'+a]), (O['go_'+a]-O['py_'+a]).abs().median()))
print('spearman score by index', O.groupby('index').apply(lambda g: g.go_score.corr(g.py_score, method='spearman')).round(3).to_dict())
