import os, sys, numpy as np, pandas as pd
HERE = os.path.dirname(os.path.abspath(__file__))
P = pd.read_pickle(os.path.join(HERE, 'panel.pkl'))
# drop warm-up dates where an index has <70% of its names scorable
cnt = P.groupby(['date', 'index']).size().unstack()
full = cnt.max()
good = (cnt >= 0.7 * full).all(axis=1)
P = P[P.date.isin(good[good].index)].copy()
SIGS = ['score', 'trend', 'mom12_1', 'ret63', 'strz', 'stretch21', 'rev5', 'rev21', 'hi52', 'lowvol', 'indmom',
        'idio_rev5', 'overnight21', 'intraday21']
HS = [5, 10, 15]
DATES = np.sort(P.date.unique())
MID = DATES[len(DATES) // 2]
lines = []
def out(s=''):
    print(s); lines.append(str(s))

out(f'Dates: {len(DATES)} weekly, {pd.Timestamp(DATES[0]).date()} .. {pd.Timestamp(DATES[-1]).date()}; split at {pd.Timestamp(MID).date()}')
out(f'Rows: {len(P)}; names per index: {P.groupby("index").ticker.nunique().to_dict()}')

# ---------- per (date,index) IC and quintile spreads ----------
def rank_ic(g, s, y):
    m = g[[s, y]].dropna()
    if len(m) < 15:
        return np.nan
    return m[s].rank().corr(m[y].rank())

def q_spread(g, s, y):
    m = g[[s, y]].dropna()
    if len(m) < 15:
        return np.nan
    q = pd.qcut(m[s].rank(method='first'), 5, labels=False)
    return m[y][q == 4].mean() - m[y][q == 0].mean()

rec = []
for (d, idx), g in P.groupby(['date', 'index']):
    reg = g.region.iloc[0]
    for s in SIGS:
        r = dict(date=d, index=idx, region=reg, sig=s)
        for h in HS:
            r[f'ic{h}'] = rank_ic(g, s, f'xs{h}')
        r['qs10'] = q_spread(g, s, 'xs10')
        r['qs15'] = q_spread(g, s, 'xs15')
        rec.append(r)
IC = pd.DataFrame(rec)
IC.to_pickle(os.path.join(HERE, 'ic.pkl'))

def nw_t(x, lags):
    x = np.asarray(x, float); x = x[np.isfinite(x)]
    n = len(x)
    if n < 10: return np.nan
    e = x - x.mean()
    v = e @ e / n
    for L in range(1, lags + 1):
        w = 1 - L / (lags + 1)
        v += 2 * w * (e[L:] @ e[:-L]) / n
    return x.mean() / np.sqrt(v / n)

def nonoverlap_t(x, step):
    x = np.asarray(x, float)
    ts = []
    for off in range(step):
        y = x[off::step]; y = y[np.isfinite(y)]
        if len(y) > 5: ts.append(y.mean() / (y.std(ddof=1) / np.sqrt(len(y))))
    return np.mean(ts) if ts else np.nan

def summarize(ic, label):
    # average across indices in the group per date -> time series
    ts = ic.groupby(['sig', 'date'])[[f'ic{h}' for h in HS] + ['qs10', 'qs15']].mean()
    rows = []
    for s in SIGS:
        t = ts.loc[s].sort_index()
        r = {'signal': s}
        for h in HS:
            x = t[f'ic{h}']
            lag = h // 5 + 1
            r[f'IC{h}'] = x.mean(); r[f'IR{h}'] = x.mean() / x.std()
            r[f'tNW{h}'] = nw_t(x.values, lag)
            r[f'tNO{h}'] = nonoverlap_t(x.values, max(1, h // 5))
        r['QS10%'] = 100 * t.qs10.mean()
        r['tQS10'] = nw_t(t.qs10.values, 3)
        rows.append(r)
    df = pd.DataFrame(rows).set_index('signal')
    out(f'\n=== {label} ===')
    out(df.round(3).to_string())
    return df

ALL = summarize(IC, 'ALL indices (per-date mean of per-index Spearman IC vs benchmark-excess fwd return)')
for reg in ['US', 'EU', 'Asia']:
    summarize(IC[IC.region == reg], f'Region {reg}')
summarize(IC[IC.date < MID], 'First half')
summarize(IC[IC.date >= MID], 'Second half')

# compact stability table at h=10/15
out('\n=== Stability: mean IC10 / IC15 by region and half ===')
st = IC.assign(half=np.where(IC.date < MID, 'H1', 'H2'))
tab = pd.concat({
    'US': IC[IC.region == 'US'].groupby('sig').ic10.mean(),
    'EU': IC[IC.region == 'EU'].groupby('sig').ic10.mean(),
    'Asia': IC[IC.region == 'Asia'].groupby('sig').ic10.mean(),
    'H1': st[st.half == 'H1'].groupby('sig').ic10.mean(),
    'H2': st[st.half == 'H2'].groupby('sig').ic10.mean(),
    'US15': IC[IC.region == 'US'].groupby('sig').ic15.mean(),
    'EU15': IC[IC.region == 'EU'].groupby('sig').ic15.mean(),
    'Asia15': IC[IC.region == 'Asia'].groupby('sig').ic15.mean(),
    'H1_15': st[st.half == 'H1'].groupby('sig').ic15.mean(),
    'H2_15': st[st.half == 'H2'].groupby('sig').ic15.mean()}, axis=1).loc[SIGS]
out(tab.round(3).to_string())

# ---------- OOS combined signal ----------
CAND = [s for s in SIGS if s not in ('score', 'trend')]
ts10 = IC.groupby(['sig', 'date']).ic10.mean().unstack(0)
P['rk'] = 0.0
comb_rows = []
dates = list(DATES)
for i, d in enumerate(dates):
    d = pd.Timestamp(d)
    lo = d - pd.Timedelta(days=365)
    hi = d - pd.Timedelta(days=21)  # xs10 of those dates realized by d (10 sessions + holidays)
    win = ts10[(ts10.index > lo) & (ts10.index <= hi)]
    if len(win) < 26:
        continue
    ir = win.mean() / win.std()
    sel = [s for s in CAND if ir.get(s, 0) > 0]
    selflip = [(s, np.sign(ir[s])) for s in CAND if abs(ir.get(s, 0)) > 0.1]
    g = P[P.date == d]
    for idx, gi in g.groupby('index'):
        rk = gi[sel].rank(pct=True).mean(axis=1) if sel else pd.Series(np.nan, gi.index)
        rf = sum(sgn * gi[s].rank(pct=True) for s, sgn in selflip) / max(1, len(selflip)) if selflip else pd.Series(np.nan, gi.index)
        for k in gi.index:
            comb_rows.append((k, rk[k], rf[k]))
    out(f'OOS {d.date()} selected: {",".join(sel)}') if i % 26 == 0 else None
C = pd.DataFrame(comb_rows, columns=['k', 'comb', 'combflip']).set_index('k')
P = P.join(C)
oos = P[P.comb.notna()]
out(f'\n=== OOS combined signal ({oos.date.nunique()} dates, {pd.Timestamp(oos.date.min()).date()}..) ===')
res = []
for s in ['comb', 'combflip', 'score', 'rev5', 'rev21', 'idio_rev5']:
    r = {'signal': s}
    per = []
    for (d, idx), g in oos.groupby(['date', 'index']):
        m = g[[s, 'xs10', 'xs15']].dropna()
        if len(m) < 15: continue
        q = pd.qcut(m[s].rank(method='first'), 5, labels=False)
        per.append(dict(date=d, ic10=m[s].rank().corr(m.xs10.rank()), ic15=m[s].rank().corr(m.xs15.rank()),
                        ls10=m.xs10[q == 4].mean() - m.xs10[q == 0].mean(), top10=m.xs10[q == 4].mean()))
    t = pd.DataFrame(per).groupby('date').mean()
    r.update(IC10=t.ic10.mean(), tNW10=nw_t(t.ic10.values, 3), IC15=t.ic15.mean(), tNW15=nw_t(t.ic15.values, 4),
             LS10_gross_pct=100 * t.ls10.mean(), LS10_net60bp=100 * t.ls10.mean() - 0.60,
             tLS10=nw_t(t.ls10.values, 3), TopQ10_net30bp=100 * t.top10.mean() - 0.30)
    res.append(r)
out(pd.DataFrame(res).set_index('signal').round(3).to_string())
out('comb = rank-avg of signals with trailing-52w ICIR(10d)>0; combflip = sign-weighted rank-avg of signals with |ICIR|>0.1.')
out('Costs: L-S net subtracts 30bp round trip on EACH leg (60bp); top-quintile net subtracts 30bp. Each 10-session hold charged in full.')

open(os.path.join(HERE, 'report_ic.txt'), 'w').write('\n'.join(lines))
