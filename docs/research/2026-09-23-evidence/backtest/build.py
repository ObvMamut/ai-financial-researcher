# Build point-in-time signal panel + forward excess returns from cached Yahoo bars.
import csv, json, os, numpy as np, pandas as pd
HERE = os.path.dirname(os.path.abspath(__file__))
RAW = os.path.join(HERE, 'raw')
REPO = '/home/mamut/all/programming/claude-financial-researcher/internal/universe/data'
IDX_BENCH = {'sp500': '^GSPC', 'nq100': '^NDX', 'eu50': '^STOXX50E', 'asia100': '^N225'}
MKT = {'T': '^N225', 'HK': '^HSI', 'NS': '^NSEI', 'AX': '^AXJO', 'TW': '^TWII', 'KS': '^KS11', 'SI': '^STI', 'BK': '^SET.BK'}
REGION = {'sp500': 'US', 'nq100': 'US', 'eu50': 'EU', 'asia100': 'Asia'}
HS = [5, 10, 15]


def bench_for(idx, t):
    if idx != 'asia100':
        return IDX_BENCH[idx]
    suf = t.rsplit('.', 1)[-1].upper() if '.' in t else ''
    return MKT.get(suf, '^N225')


def load(sym):
    d = json.load(open(os.path.join(RAW, sym.replace('^', '_') + '.json')))['chart']['result'][0]
    off = d['meta'].get('gmtoffset', 0)
    q = d['indicators']['quote'][0]
    adj = d['indicators'].get('adjclose', [{}])[0].get('adjclose')
    df = pd.DataFrame({'o': q['open'], 'h': q['high'], 'l': q['low'], 'c': q['close'],
                       'a': adj if adj else q['close']},
                      index=pd.to_datetime(np.array(d['timestamp']) + off, unit='s').normalize())
    df = df[~df.index.duplicated(keep='last')].dropna(subset=['c'])
    df = df[df.c > 0]
    f = (df.a / df.c).fillna(1.0)
    for k in 'ohl':
        df[k] = df[k] * f
    df['c'] = df['a']
    df[['o', 'h', 'l']] = df[['o', 'h', 'l']].where(df[['o', 'h', 'l']] > 0)
    return df[['o', 'h', 'l', 'c']]


def yz20(df):
    n = 20
    pc = df.c.shift(1)
    on = np.log(df.o / pc)
    oc = np.log(df.c / df.o)
    rs = np.log(df.h / df.o) * np.log(df.h / df.c) + np.log(df.l / df.o) * np.log(df.l / df.c)
    k = 0.34 / (1.34 + (n + 1) / (n - 1))
    v = on.rolling(n, min_periods=15).var() + k * oc.rolling(n, min_periods=15).var() + (1 - k) * rs.rolling(n, min_periods=15).mean()
    return np.sqrt(v.where(v > 0))  # daily sigma


def signals(df, bench):
    c = df.c
    lr = np.log(c / c.shift(1))
    s = pd.DataFrame(index=df.index)
    s['ret5'] = c / c.shift(5) - 1
    s['ret21'] = c / c.shift(21) - 1
    s['ret63'] = c / c.shift(63) - 1
    s['mom12_1'] = c.shift(21) / c.shift(252) - 1
    r5 = lr.rolling(5).sum()
    m = r5.rolling(248, min_periods=26).mean()
    sd = r5.rolling(248, min_periods=26).std()
    s['strz'] = (r5 - m) / sd
    s['sigma'] = yz20(df)
    s['stretch21'] = s.ret21 / (s.sigma * np.sqrt(21))
    s['hi52'] = c / df.h.rolling(252, min_periods=200).max()
    s['lowvol'] = -lr.rolling(63).std()
    # beta vs own benchmark, aligned on common dates, 252d
    bc = bench.c.reindex(df.index, method='ffill')
    blr = np.log(bc / bc.shift(1))
    cov = lr.rolling(252, min_periods=120).cov(blr)
    beta = cov / blr.rolling(252, min_periods=120).var()
    resid5 = r5 - beta * blr.rolling(5).sum()
    s['idio_rev5'] = -resid5
    s['rev5'] = -s.ret5
    s['rev21'] = -s.ret21
    s['overnight21'] = np.log(df.o / c.shift(1)).rolling(21).sum()
    s['intraday21'] = np.log(c / df.o).rolling(21).sum()
    s['nbars'] = np.arange(1, len(df) + 1)
    return s


def zs(x):
    x = x.astype(float)
    n = len(x)
    if n < 2:
        return x * 0
    if n >= 5:
        srt = np.sort(x.values)
        k = max(1, int(0.02 * n))
        lo, hi = srt[k], srt[n - 1 - k]
        x = x.clip(lo, hi)
    sd = x.std()
    if not np.isfinite(sd) or sd <= 1e-12 * max(1, abs(x.mean())):
        return x * 0
    return (x - x.mean()) / sd


def composite(g):
    zm = zs(g.mom12_1)
    zr = zs(g.ret63)
    trend = 0.5 * zm + zr
    sc = trend.copy()
    ss = lambda a, b: ((a > 0) & (b > 0)) | ((a < 0) & (b < 0))
    sc = sc - np.where(ss(g.strz, trend), 0.5 * g.strz, 0)
    sc = sc - np.where(ss(g.stretch21, trend), 0.35 * g.stretch21, 0)
    sc = np.where((trend > 0) & (sc < 0), 0, sc)
    sc = np.where((trend < 0) & (sc > 0), 0, sc)
    return pd.Series(sc, index=g.index), trend


def main():
    members = []
    for idx in IDX_BENCH:
        rows = [l for l in open(f'{REPO}/{idx}.csv') if not l.startswith('#')]
        for r in csv.DictReader(rows):
            members.append((idx, r['ticker'].strip(), r['sector'].strip()))
    benches = {b: load(b) for b in set(IDX_BENCH.values()) | set(MKT.values())}
    cache, sig = {}, {}
    for idx, t, sec in members:
        if t not in cache:
            try:
                cache[t] = load(t)
            except Exception as e:
                print('load fail', t, e); continue
    # weekly rebalance dates: Fridays
    all_days = pd.date_range('2022-09-02', '2026-09-18', freq='W-FRI')
    recs = []
    for idx, t, sec in members:
        if t not in cache:
            continue
        df = cache[t]
        bsym = bench_for(idx, t)
        b = benches[bsym]
        key = (t, bsym)
        if key not in sig:
            sig[key] = signals(df, b)
        s = sig[key]
        pos = df.index.searchsorted(all_days, side='right') - 1
        bc = b.c
        for d, p in zip(all_days, pos):
            if p < 252:
                continue
            if (d - df.index[p]).days > 5:  # stale: no bar in the week
                continue
            row = s.iloc[p].to_dict()
            row.update(date=d, index=idx, ticker=t, sector=sec, region=REGION[idx], bench=bsym, p=p)
            d0 = df.index[p]
            b0 = bc.asof(d0)
            for h in HS:
                if p + h < len(df):
                    d1 = df.index[p + h]
                    r = df.c.iloc[p + h] / df.c.iloc[p] - 1
                    br = bc.asof(d1) / b0 - 1
                    row[f'fwd{h}'] = r
                    row[f'xs{h}'] = r - br
                else:
                    row[f'fwd{h}'] = row[f'xs{h}'] = np.nan
            recs.append(row)
    P = pd.DataFrame(recs)
    # composite within index/date
    P['score'] = np.nan
    P['trend'] = np.nan
    ok = P[['mom12_1', 'ret63', 'strz', 'stretch21']].notna().all(axis=1)
    for (d, idx), g in P[ok].groupby(['date', 'index']):
        sc, tr = composite(g)
        P.loc[g.index, 'score'] = sc
        P.loc[g.index, 'trend'] = tr
    # industry momentum: sector mean ret63 within region/date (unique tickers)
    u = P.drop_duplicates(['date', 'region', 'ticker'])
    im = u.groupby(['date', 'region', 'sector']).ret63.mean().rename('indmom')
    P = P.join(im, on=['date', 'region', 'sector'])
    P.to_pickle(os.path.join(HERE, 'panel.pkl'))
    print(P.shape, P.date.min(), P.date.max(), P.groupby('index').ticker.nunique().to_dict())
    # barrier path data: save the price frames needed
    pd.to_pickle({'cache': cache}, os.path.join(HERE, 'prices.pkl'))


if __name__ == '__main__':
    main()
