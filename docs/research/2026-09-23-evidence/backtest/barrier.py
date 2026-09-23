import os, numpy as np, pandas as pd
HERE = os.path.dirname(os.path.abspath(__file__))
P = pd.read_pickle(os.path.join(HERE, 'panel.pkl'))
cache = pd.read_pickle(os.path.join(HERE, 'prices.pkl'))['cache']
cnt = P.groupby(['date', 'index']).size().unstack()
good = (cnt >= 0.7 * cnt.max()).all(axis=1)
P = P[P.date.isin(good[good].index)]
H = 15
COST = 0.003
KS = [0.75, 1.0, 1.25, 1.5, 2.0]
KT = [1.0, 1.5, 2.0, 2.5, 3.0, None]

picks = []
for (d, idx), g in P.groupby(['date', 'index']):
    g = g[g.score.notna() & (g.score != 0) & g.sigma.notna()]
    picks.append(g.loc[g.score.abs().sort_values(ascending=False).index[:5]])
T = pd.concat(picks)
T['dir'] = np.sign(T.score)
dates = np.sort(T.date.unique())
MID = dates[len(dates) // 2]

# pre-extract paths: next-open entry, then 15 sessions of O/H/L/C
paths = []
keep = []
for k, r in T.iterrows():
    df = cache[r.ticker]
    p = int(r.p)
    if p + H >= len(df):
        continue
    w = df.iloc[p + 1:p + 1 + H]
    if w[['o', 'h', 'l', 'c']].isna().any().any():
        w = w.ffill(axis=1)
        if w.isna().any().any():
            continue
    paths.append(w[['o', 'h', 'l', 'c']].values)
    keep.append(k)
T = T.loc[keep]
A = np.stack(paths)  # n x H x 4
D = T.dir.values
E = A[:, 0, 0]
SIG = T.sigma.values
print(f'trades: {len(T)}, longs {np.mean(D > 0):.0%}, dates {len(dates)}, split {pd.Timestamp(MID).date()}')


def simulate(stop_frac, tgt_frac):
    """stop_frac/tgt_frac: arrays (per trade) of distance as fraction of entry; tgt may be inf."""
    n = len(D)
    ret = np.empty(n); kind = np.empty(n, dtype='U1')
    for i in range(n):
        e, dr = E[i], D[i]
        sp = e * (1 - dr * stop_frac[i]); tp = e * (1 + dr * tgt_frac[i]) if np.isfinite(tgt_frac[i]) else None
        ex, kd = None, 'T'
        for j in range(H):
            o, h, l, c = A[i, j]
            if j > 0:  # gap through a barrier at the open
                if (dr > 0 and o <= sp) or (dr < 0 and o >= sp):
                    ex, kd = o, 'S'; break
                if tp is not None and ((dr > 0 and o >= tp) or (dr < 0 and o <= tp)):
                    ex, kd = o, 'W'; break
            hit_s = (l <= sp) if dr > 0 else (h >= sp)
            hit_t = tp is not None and ((h >= tp) if dr > 0 else (l <= tp))
            if hit_s:  # both same day: assume stop first (conservative)
                ex, kd = sp, 'S'; break
            if hit_t:
                ex, kd = tp, 'W'; break
        if ex is None:
            ex = A[i, H - 1, 3]
        ret[i] = dr * (ex / e - 1) - COST
        kind[i] = kd
    return ret, kind


sq = SIG * np.sqrt(H)
rows = []
first = T.date.values < MID
for ks in KS:
    for kt in KT:
        r, kd = simulate(ks * sq, (kt * sq) if kt else np.full(len(D), np.inf))
        for lab, m in [('all', np.ones(len(D), bool)), ('H1', first), ('H2', ~first)]:
            rows.append(dict(ks=ks, kt=kt if kt else 'none', part=lab, mean_pct=100 * r[m].mean(),
                             hit=np.mean(r[m] > 0), stop_share=np.mean(kd[m] == 'S'), tgt_share=np.mean(kd[m] == 'W'),
                             time_share=np.mean(kd[m] == 'T'), n=m.sum()))
# current practice: fixed 9% stop / 15% target
r, kd = simulate(np.full(len(D), 0.09), np.full(len(D), 0.15))
for lab, m in [('all', np.ones(len(D), bool)), ('H1', first), ('H2', ~first)]:
    rows.append(dict(ks='9%', kt='15%', part=lab, mean_pct=100 * r[m].mean(), hit=np.mean(r[m] > 0),
                     stop_share=np.mean(kd[m] == 'S'), tgt_share=np.mean(kd[m] == 'W'), time_share=np.mean(kd[m] == 'T'), n=m.sum()))
# pure 15-session hold, no barriers
r, kd = simulate(np.full(len(D), np.inf), np.full(len(D), np.inf))
for lab, m in [('all', np.ones(len(D), bool)), ('H1', first), ('H2', ~first)]:
    rows.append(dict(ks='none', kt='none', part=lab, mean_pct=100 * r[m].mean(), hit=np.mean(r[m] > 0),
                     stop_share=0, tgt_share=0, time_share=1, n=m.sum()))
R = pd.DataFrame(rows)
R.to_csv(os.path.join(HERE, 'barrier_grid.csv'), index=False)
W = R.pivot_table(index=['ks', 'kt'], columns='part', values='mean_pct', sort=False)
lines = [f'Top-5 |composite| per index per week, n={len(T)} trades, entry next open, 30bp round-trip cost, H=15, stop-first on same-day double touch',
         f'median sigma_daily {np.median(SIG):.4f}; median stop at ks=1: {100*np.median(sq):.1f}%',
         '\nMean net return per trade (%) by part:', W.round(3).to_string()]
allr = R[R.part == 'all'].set_index(['ks', 'kt'])[['mean_pct', 'hit', 'stop_share', 'tgt_share', 'time_share']]
lines += ['\nFull-sample detail:', allr.round(3).to_string()]
h1 = R[(R.part == 'H1') & ~R.ks.isin(['9%', 'none'])].sort_values('mean_pct', ascending=False).iloc[0]
h2 = R[(R.part == 'H2') & (R.ks == h1.ks) & (R.kt == h1.kt)].iloc[0]
cur2 = R[(R.part == 'H2') & (R.ks == '9%')].iloc[0]
none2 = R[(R.part == 'H2') & (R.ks == 'none')].iloc[0]
h2best = R[(R.part == 'H2') & ~R.ks.isin(['9%', 'none'])].sort_values('mean_pct', ascending=False).iloc[0]
lines += [f'\nWalk-forward: H1 best ks={h1.ks} kt={h1.kt} ({h1.mean_pct:.3f}% in H1) -> H2 {h2.mean_pct:.3f}% hit {h2.hit:.2f} time-exit {h2.time_share:.2f}',
          f'H2 current practice 9%/15%: {cur2.mean_pct:.3f}% hit {cur2.hit:.2f} time-exit {cur2.time_share:.2f}',
          f'H2 no barriers (15-session hold): {none2.mean_pct:.3f}% hit {none2.hit:.2f}',
          f'H2 in-sample best (for reference, not OOS): ks={h2best.ks} kt={h2best.kt} {h2best.mean_pct:.3f}%',
          f'Per-trade sd of net return (no barriers): {100*np.std(simulate(np.full(len(D), np.inf), np.full(len(D), np.inf))[0]):.2f}%']
# long vs short split for current practice & no barriers
r0, _ = simulate(np.full(len(D), np.inf), np.full(len(D), np.inf))
lines.append(f'No-barrier mean by side: long {100*r0[D>0].mean():.3f}% (n={np.sum(D>0)}), short {100*r0[D<0].mean():.3f}% (n={np.sum(D<0)})')
txt = '\n'.join(lines)
print(txt)
open(os.path.join(HERE, 'report_barrier.txt'), 'w').write(txt)
