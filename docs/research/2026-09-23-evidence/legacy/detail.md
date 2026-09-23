# Legacy pipeline per-stage analysis (2026-09-23)
Scope: 26 legacy independent runs (2026-06-01..2026-09-06), thesis/single excluded. Outcomes = call excess vs benchmark, direction-aligned, weekly dedup (ticker,direction, 7d, earliest wins).
Outcome source: control --json call_excess_pct where present, else own Yahoo recomputation (validated: corr 0.975-0.99 vs control, mean abs diff 0.6-1.3pp).
old era = runs before 2026-08-28 (technicals domain, no prescreen, 24-38-name shortlists); new era = 08-28..09-06.
Files: load.py build.py an1.py(domain IC) an2.py(chief) an3.py(breakdowns) an5.py(trade construction) rows.pkl shipped_trades.csv px/ (Yahoo bars).
Unfilled vs filled shipped ideas, call excess H10 (not dedup): unfilled n=27 +3.09 ; filled n=88 -0.90. H15: unfilled 18 +0.63; filled 66 +1.26.


## H=10 shortlist (dedup, non-neutral bias) n=261 mean=0.75 CI(wk-cluster)=(np.float64(-0.66), np.float64(1.9))
domain | era | n scored | IC (spearman aligned score vs excess) | p | agree n/mean | disagree n/mean | neutral(0) n/mean | missing n/mean
quant | all | 235 | +0.049 | 0.45 | 136/+0.32 | 48/+0.83 | 51/+1.70 | 26/+0.96
news | all | 245 | -0.077 | 0.23 | 157/+0.32 | 39/+1.84 | 49/+1.88 | 16/-1.17
fundamentals | all | 246 | +0.029 | 0.66 | 128/+1.03 | 53/+0.11 | 65/+1.01 | 15/-0.57
sentiment | all | 237 | +0.067 | 0.31 | 133/+1.50 | 41/-0.04 | 63/+0.42 | 24/-1.22
macro | all | 250 | -0.005 | 0.93 | 128/+0.40 | 60/+1.16 | 62/+1.44 | 11/-1.34
base(aligned, run weights) | all | 261 | +0.017 | 0.79
composite(aligned) | all | 40 | -0.044 | 0.79
quant | old | 171 | +0.044 | 0.57 | 96/+0.54 | 39/+1.17 | 36/+2.67 | 26/+0.96
news | old | 194 | -0.117 | 0.11 | 126/+0.29 | 37/+2.15 | 31/+3.77 | 3/-4.96
fundamentals | old | 197 | -0.006 | 0.93 | 111/+1.03 | 39/+0.63 | 47/+1.70 | 0/+nan
sentiment | old | 195 | +0.039 | 0.59 | 119/+1.41 | 35/+0.41 | 41/+1.16 | 2/-5.89
macro | old | 197 | -0.005 | 0.95 | 93/+0.72 | 60/+1.16 | 44/+1.86 | 0/+nan
base(aligned, run weights) | old | 197 | -0.016 | 0.83
quant | new | 64 | +0.103 | 0.42 | 40/-0.19 | 9/-0.66 | 15/-0.65 | 0/+nan
news | new | 51 | +0.145 | 0.31 | 31/+0.42 | 2/-3.86 | 18/-1.36 | 13/-0.30
fundamentals | new | 49 | +0.142 | 0.33 | 17/+1.08 | 14/-1.33 | 18/-0.80 | 15/-0.57
sentiment | new | 42 | +0.196 | 0.21 | 14/+2.24 | 6/-2.67 | 22/-0.95 | 22/-0.80
macro | new | 53 | +0.070 | 0.62 | 35/-0.46 | 0/+nan | 18/+0.42 | 11/-1.34
base(aligned, run weights) | new | 64 | +0.189 | 0.14
composite(aligned) | new | 40 | -0.044 | 0.79
base_al terciles (all eras): {Interval(-0.891, 0.123, closed='right'): {'count': 87, 'mean': 0.97}, Interval(0.123, 0.425, closed='right'): {'count': 87, 'mean': 1.08}, Interval(0.425, 0.895, closed='right'): {'count': 87, 'mean': 0.19}}
OLS new-era ex10 ~ comp_al + quant(missing=0): coef +0.989%/pt t=+2.14 n=40
OLS new-era ex10 ~ comp_al + news(missing=0): coef +0.395%/pt t=+0.88 n=40
OLS new-era ex10 ~ comp_al + fundamentals(missing=0): coef +0.264%/pt t=+0.64 n=40
OLS new-era ex10 ~ comp_al + sentiment(missing=0): coef +0.712%/pt t=+1.49 n=40
OLS new-era ex10 ~ comp_al + macro(missing=0): coef -0.118%/pt t=-0.16 n=40

## H=15 shortlist (dedup, non-neutral bias) n=241 mean=1.42 CI(wk-cluster)=(np.float64(0.28), np.float64(2.16))
domain | era | n scored | IC (spearman aligned score vs excess) | p | agree n/mean | disagree n/mean | neutral(0) n/mean | missing n/mean
quant | all | 215 | -0.080 | 0.24 | 120/+0.71 | 46/+3.30 | 49/+1.54 | 26/+1.10
news | all | 232 | -0.064 | 0.33 | 146/+1.18 | 39/+1.95 | 47/+2.08 | 9/-0.51
fundamentals | all | 235 | +0.013 | 0.84 | 126/+1.15 | 48/+0.79 | 61/+2.20 | 6/+4.19
sentiment | all | 230 | +0.026 | 0.69 | 131/+1.78 | 38/+1.11 | 61/+0.99 | 11/+0.57
macro | all | 232 | +0.008 | 0.91 | 113/+0.91 | 60/+0.60 | 59/+2.80 | 9/+4.12
base(aligned, run weights) | all | 241 | +0.008 | 0.90
composite(aligned) | all | 20 | +0.021 | 0.93
quant | old | 171 | -0.096 | 0.21 | 96/-0.05 | 39/+3.35 | 36/+1.64 | 26/+1.10
news | old | 194 | -0.088 | 0.22 | 126/+0.41 | 37/+2.34 | 31/+3.41 | 3/-9.91
fundamentals | old | 197 | +0.015 | 0.83 | 111/+0.84 | 39/-0.24 | 47/+2.75 | 0/+nan
sentiment | old | 195 | +0.035 | 0.63 | 119/+1.55 | 35/+0.98 | 41/+0.30 | 2/-8.32
macro | old | 197 | +0.009 | 0.90 | 93/+0.48 | 60/+0.60 | 44/+3.01 | 0/+nan
base(aligned, run weights) | old | 197 | -0.009 | 0.89
quant | new | 44 | +0.165 | 0.28 | 24/+3.76 | 7/+3.02 | 13/+1.28 | 0/+nan
news | new | 38 | +0.415 | 0.01 | 20/+6.05 | 2/-5.16 | 16/-0.49 | 6/+4.19
fundamentals | new | 38 | -0.030 | 0.86 | 15/+3.40 | 9/+5.26 | 14/+0.32 | 6/+4.19
sentiment | new | 35 | +0.089 | 0.61 | 12/+4.10 | 3/+2.57 | 20/+2.40 | 9/+2.55
macro | new | 35 | +0.065 | 0.71 | 20/+2.90 | 0/+nan | 15/+2.19 | 9/+4.12
base(aligned, run weights) | new | 44 | +0.217 | 0.16
composite(aligned) | new | 20 | +0.021 | 0.93
base_al terciles (all eras): {Interval(-0.891, 0.12, closed='right'): {'count': 81, 'mean': 1.62}, Interval(0.12, 0.455, closed='right'): {'count': 80, 'mean': 2.11}, Interval(0.455, 0.895, closed='right'): {'count': 80, 'mean': 0.52}}
OLS new-era ex15 ~ comp_al + quant(missing=0): coef -1.520%/pt t=-0.66 n=20
OLS new-era ex15 ~ comp_al + news(missing=0): coef +1.262%/pt t=+1.80 n=20
OLS new-era ex15 ~ comp_al + fundamentals(missing=0): coef -0.357%/pt t=-0.56 n=20
OLS new-era ex15 ~ comp_al + sentiment(missing=0): coef +0.406%/pt t=+0.49 n=20
OLS new-era ex15 ~ comp_al + macro(missing=0): coef -2.131%/pt t=-1.65 n=20

######## H=10
[2] all shortlist@bias shipped: n=60 mean=+0.55 hit=50% CI=(np.float64(-1.12), np.float64(2.0)) | not shipped: n=190 mean=+0.58 hit=54% CI=(np.float64(-1.1), np.float64(1.84))
[2] old shortlist@bias shipped: n=33 mean=+0.44 hit=55% CI=(np.float64(-2.36), np.float64(2.6)) | not shipped: n=153 mean=+1.00 hit=54% CI=(np.float64(-0.87), np.float64(2.16))
[2] new shortlist@bias shipped: n=27 mean=+0.70 hit=44% CI=(np.float64(0.63), np.float64(0.74)) | not shipped: n=37 mean=-1.14 hit=54% CI=(np.float64(-2.16), np.float64(0.55))
[2] all shipped@ship dir: n=76 mean=+0.15 hit=50% CI=(np.float64(-1.0), np.float64(1.72))
[2] old shipped@ship dir: n=39 mean=+0.60 hit=54% CI=(np.float64(-1.63), np.float64(2.53))
[2] new shipped@ship dir: n=37 mean=-0.31 hit=46% CI=(np.float64(-0.59), np.float64(0.34))
[2] shipped against scout bias: n=9 mean=-1.16
[2] shipped IC conf vs sx10: rho=-0.162 p=0.43 n=26
[2] shipped IC base_conf vs sx10: rho=-0.163 p=0.43 n=26
[2] shipped IC adj vs sx10: rho=+0.187 p=0.36 n=26
[2] adj buckets: {Interval(-99.0, -3.0, closed='right'): {'count': 8, 'mean': -3.09}, Interval(-3.0, -0.5, closed='right'): {'count': 1, 'mean': -5.63}, Interval(-0.5, 0.5, closed='right'): {'count': 14, 'mean': -0.34}, Interval(0.5, 3.0, closed='right'): {'count': 2, 'mean': 9.12}, Interval(3.0, 99.0, closed='right'): {'count': 1, 'mean': 1.5}}
[2] adj describe {'count': 26.0, 'mean': -1.08, 'std': 2.71, 'min': -8.0, '25%': -3.0, '50%': 0.0, '75%': 0.0, 'max': 4.0}
[2] shipped IC confidence (all eras) rho=+0.033 p=0.78 n=76
[2] by rank: {1: {'count': 13, 'mean': 2.27}, 2: {'count': 14, 'mean': 0.69}, 3: {'count': 19, 'mean': 1.0}, 4: {'count': 15, 'mean': -3.0}, 5: {'count': 15, 'mean': -0.1}}
[3] by setup:
              count_short  mean_short  count_ship  mean_ship
setup                                                       
?                     246        0.76          67      -0.35
base                    3        1.95           2       0.39
continuation            5       -0.28           3       5.40
drift                   3        1.47           2       6.40
pullback                4        0.01           2       2.88
[3] by dir:
       count_short  mean_short  count_ship  mean_ship
dir                                                  
long           202        0.49          59       0.41
short           59        1.61          17      -0.74
[3] by region:
        count_short  mean_short  count_ship  mean_ship
region                                                
asia             20        1.37          20       0.21
eu               24        1.22          16      -0.26
us              217        0.64          40       0.29
[3] by sector:
                        count_short  mean_short  count_ship  mean_ship
sector                                                                
?                               221        1.05        50.0       0.54
Communication Services            3       -8.76         3.0      -7.47
Consumer Discretionary            6       -1.13         5.0       1.27
Consumer Staples                  2       -1.41         NaN        NaN
Energy                            1        4.69         1.0       4.69
Financials                        5       -0.37         3.0       0.75
Health Care                       6       -1.21         4.0      -4.00
Information Technology           15       -0.11         9.0       1.00
Materials                         2        2.37         1.0       0.87
[3] by era:
     count_short  mean_short  count_ship  mean_ship
era                                                
new           64       -0.36          37      -0.31
old          197        1.11          39       0.60
[4] scout bias vs composite sign:
                    count  mean
agree setup                    
False drift             1 -1.29
True  ?                25 -1.84
      base              3  1.95
      continuation      5 -0.28
      drift             2  2.85
      pullback          4  0.01
[4] totals: {False: {'count': 1, 'mean': -1.29}, True: {'count': 39, 'mean': -0.92}}
[3] nominations: {1: {'count': 260, 'mean': 0.77}, 2: {'count': 1, 'mean': -5.97}}

######## H=15
[2] all shortlist@bias shipped: n=51 mean=+0.95 hit=63% CI=(np.float64(-1.1), np.float64(2.27)) | not shipped: n=179 mean=+1.40 hit=51% CI=(np.float64(-0.3), np.float64(2.53))
[2] old shortlist@bias shipped: n=33 mean=+0.42 hit=58% CI=(np.float64(-3.1), np.float64(2.46)) | not shipped: n=153 mean=+1.03 hit=48% CI=(np.float64(-1.09), np.float64(2.1))
[2] new shortlist@bias shipped: n=18 mean=+1.91 hit=72% CI=(np.float64(1.74), np.float64(2.12)) | not shipped: n=26 mean=+3.60 hit=65% CI=(np.float64(2.57), np.float64(4.8))
[2] all shipped@ship dir: n=62 mean=+0.75 hit=58% CI=(np.float64(-0.66), np.float64(1.98))
[2] old shipped@ship dir: n=39 mean=+0.14 hit=49% CI=(np.float64(-1.47), np.float64(1.36))
[2] new shipped@ship dir: n=23 mean=+1.79 hit=74% CI=(np.float64(0.1), np.float64(3.34))
[2] shipped against scout bias: n=8 mean=-0.29
[2] shipped IC conf vs sx15: rho=+0.002 p=1.00 n=12
[2] shipped IC base_conf vs sx15: rho=+0.009 p=0.98 n=12
[2] shipped IC adj vs sx15: rho=+0.022 p=0.95 n=12
[2] adj buckets: {Interval(-99.0, -3.0, closed='right'): {'count': 2, 'mean': 4.16}, Interval(-0.5, 0.5, closed='right'): {'count': 10, 'mean': 3.18}}
[2] adj describe {'count': 12.0, 'mean': -0.75, 'std': 1.86, 'min': -6.0, '25%': 0.0, '50%': 0.0, '75%': 0.0, 'max': 0.0}
[2] shipped IC confidence (all eras) rho=-0.079 p=0.54 n=62
[2] by rank: {1: {'count': 10, 'mean': 1.48}, 2: {'count': 12, 'mean': 3.04}, 3: {'count': 15, 'mean': 2.37}, 4: {'count': 12, 'mean': -3.2}, 5: {'count': 13, 'mean': -0.12}}
[3] by setup:
       count_short  mean_short  count_ship  mean_ship
setup                                                
?              241        1.42          62       0.75
[3] by dir:
       count_short  mean_short  count_ship  mean_ship
dir                                                  
long           188        1.07          51      -0.18
short           53        2.64          11       5.09
[3] by region:
        count_short  mean_short  count_ship  mean_ship
region                                                
asia             14        5.31          17       1.16
eu               21        3.42          14       1.65
us              206        0.95          31       0.13
[3] by sector:
                        count_short  mean_short  count_ship  mean_ship
sector                                                                
?                               221        1.21          50       0.13
Communication Services            2        6.42           1      12.28
Consumer Discretionary            3        6.57           3       6.32
Energy                            1        6.22           1       6.22
Financials                        2        2.05           2       2.05
Health Care                       4        1.91           3      -1.25
Information Technology            8        3.01           2       1.16
[3] by era:
     count_short  mean_short  count_ship  mean_ship
era                                                
new           44        2.91          23       1.79
old          197        1.08          39       0.14
[4] scout bias vs composite sign:
             count  mean
agree setup             
True  ?         20  3.73
[4] totals: {True: {'count': 20, 'mean': 3.73}}
[3] nominations: {1: {'count': 241, 'mean': 1.42}}

######## H=10
[3] sector (all eras):
                        count_short  mean_short  count_ship  mean_ship
sector                                                                
?                                 1       -2.91         NaN        NaN
Communication Services           18       -0.65         6.0      -3.51
Consumer Discretionary           51       -0.54        14.0       2.96
Consumer Staples                  6        0.44         NaN        NaN
Energy                           11        1.76         2.0      -4.99
Financials                       24        3.26        11.0      -0.41
Health Care                      23        2.13        10.0       1.27
Industrials                      14        2.11         1.0      -0.05
Information Technology          101        0.49        29.0       0.18
Materials                         9        0.40         2.0      -3.78
Real Estate                       1       -3.05         1.0      -4.58
Utilities                         2        4.03         NaN        NaN
[3] dir era=old:
       count_short  mean_short  count_ship  mean_ship
dir                                                  
long           152        0.66          35       0.09
short           45        2.61           4       5.03
[3] dir era=new:
       count_short  mean_short  count_ship  mean_ship
dir                                                  
long            50       -0.02          24       0.88
short           14       -1.58          13      -2.51
[3] region era=old:
        count_short  mean_short  count_ship  mean_ship
region                                                
asia              6        2.52          12       0.30
eu                9        1.85           8      -1.14
us              182        1.02          19       1.52
[3] region era=new:
        count_short  mean_short  count_ship  mean_ship
region                                                
asia             14        0.88           8       0.07
eu               15        0.84           8       0.62
us               35       -1.37          21      -0.81
[3] setup (prescreen runs 08-31..09-06, dedup):
              count_short  mean_short  count_ship  mean_ship
setup                                                       
?                      29       -1.68          18      -2.95
base                    3        1.95           2       0.39
continuation            4       -0.73           2       7.35
drift                   3        1.47           2       6.40
pullback                5        6.16           2       2.88
[3] setup NOT deduped (every run-name, heavy overlap):
              count_short  mean_short  count_ship  mean_ship
setup                                                       
?                      96       -1.73          36      -2.28
base                    9        1.47           6       1.18
continuation           15        2.22           7       2.13
drift                  12        0.54           3       8.83
pullback               24        1.83           4       1.79
[4] bias vs composite sign NOT deduped:
                    count  mean
agree setup                    
False drift             2 -0.23
True  ?                96 -1.73
      base              9  1.47
      continuation     15  2.22
      drift            10  0.69
      pullback         24  1.83
[4] direction x setup NOT deduped:
                    count  mean
setup        dir               
?            long      55 -0.46
             short     41 -3.45
base         long       8  1.01
             short      1  5.11
continuation long       9 -2.22
             short      6  8.88
drift        long      12  0.54
pullback     long       9  8.47
             short     15 -2.16

######## H=15
[3] sector (all eras):
                        count_short  mean_short  count_ship  mean_ship
sector                                                                
?                                 1       -1.76         NaN        NaN
Communication Services           17        2.23         4.0       1.40
Consumer Discretionary           48       -0.78        12.0       3.85
Consumer Staples                  4        1.02         NaN        NaN
Energy                           11        0.59         2.0      -5.99
Financials                       21        4.85        10.0       1.18
Health Care                      21        5.02         9.0       2.51
Industrials                      14        2.41         1.0      -2.46
Information Technology           94        1.12        22.0      -0.59
Materials                         7       -2.08         1.0      -6.63
Real Estate                       1       -3.72         1.0      -5.27
Utilities                         2        2.06         NaN        NaN
[3] dir era=old:
       count_short  mean_short  count_ship  mean_ship
dir                                                  
long           152        0.58          35      -0.64
short           45        2.80           4       6.99
[3] dir era=new:
       count_short  mean_short  count_ship  mean_ship
dir                                                  
long            36        3.16          16       0.82
short            8        1.76           7       4.01
[3] region era=old:
        count_short  mean_short  count_ship  mean_ship
region                                                
asia              6        4.66          12       0.05
eu                9        4.74           8       0.28
us              182        0.79          19       0.15
[3] region era=new:
        count_short  mean_short  count_ship  mean_ship
region                                                
asia              8        5.80           5       3.83
eu               12        2.42           6       3.48
us               24        2.19          12       0.10
[3] setup (prescreen runs 08-31..09-06, dedup):
       count_short  mean_short  count_ship  mean_ship
setup                                                
?               23        4.43          13       3.87
[3] setup NOT deduped (every run-name, heavy overlap):
       count_short  mean_short  count_ship  mean_ship
setup                                                
?               66        3.56          25       2.51
[4] bias vs composite sign NOT deduped:
             count  mean
agree setup             
True  ?         66  3.56
[4] direction x setup NOT deduped:
             count  mean
setup dir               
?     long      37  3.33
      short     29  3.85

## all shipped (not dedup) n=116
outcomes: {'expired': 59, 'unfilled': 27, 'open': 20, 'stop': 8, 'target': 2}
fill rate (excl open-unresolved): 89/116
closed by outcome mean pnl% / R: {('pnl', 'count'): {'expired': 59, 'stop': 8, 'target': 2}, ('pnl', 'mean'): {'expired': 0.97, 'stop': -6.68, 'target': 22.85}, ('R', 'count'): {'expired': 23, 'stop': 8, 'target': 2}, ('R', 'mean'): {'expired': 0.43, 'stop': -1.02, 'target': 1.69}}
closed n=69 mean pnl=+0.71% mean R=+0.15 win=55%
stop dist (σ·√H): {'count': 66.0, 'mean': 1.21, 'std': 0.16, 'min': 0.98, '25%': 1.1, '50%': 1.19, '75%': 1.27, 'max': 1.89}
target dist (σ·√H): {'count': 66.0, 'mean': 2.19, 'std': 0.39, 'min': 1.49, '25%': 2.04, '50%': 2.19, '75%': 2.38, 'max': 3.45}
R:R: {'count': 66.0, 'mean': 1.8, 'std': 0.15, 'min': 1.51, '25%': 1.65, '50%': 1.87, '75%': 1.91, 'max': 2.0}
entry offset patient-side (σ·√5): {'count': 66.0, 'mean': 0.17, 'std': 0.19, 'min': -0.02, '25%': 0.0, '50%': 0.11, '75%': 0.31, 'max': 0.64}
timeframe H: {'count': 116.0, 'mean': 12.7, 'std': 3.2, 'min': 10.0, '25%': 10.0, '50%': 11.0, '75%': 15.0, 'max': 20.0}
by era outcomes: {'new': {'expired': 27, 'open': 19, 'stop': 7, 'target': 1, 'unfilled': 17}, 'old': {'expired': 32, 'open': 1, 'stop': 1, 'target': 1, 'unfilled': 10}}
stop_u by era: {'new': 1.17, 'old': 1.29} tgt_u by era: {'new': 2.23, 'old': 2.11}
unfilled vs entry_off (median): {False: 0.07, True: 0.24}

## dedup n=77
outcomes: {'expired': 42, 'unfilled': 18, 'open': 9, 'stop': 6, 'target': 2}
fill rate (excl open-unresolved): 59/77
closed by outcome mean pnl% / R: {('pnl', 'count'): {'expired': 42, 'stop': 6, 'target': 2}, ('pnl', 'mean'): {'expired': 0.04, 'stop': -8.35, 'target': 22.85}, ('R', 'count'): {'expired': 13, 'stop': 6, 'target': 2}, ('R', 'mean'): {'expired': 0.38, 'stop': -1.03, 'target': 1.69}}
closed n=50 mean pnl=-0.06% mean R=+0.10 win=50%
stop dist (σ·√H): {'count': 34.0, 'mean': 1.19, 'std': 0.15, 'min': 0.98, '25%': 1.09, '50%': 1.16, '75%': 1.25, 'max': 1.6}
target dist (σ·√H): {'count': 34.0, 'mean': 2.09, 'std': 0.39, 'min': 1.49, '25%': 1.71, '50%': 2.12, '75%': 2.3, 'max': 2.98}
R:R: {'count': 34.0, 'mean': 1.75, 'std': 0.17, 'min': 1.51, '25%': 1.59, '50%': 1.83, '75%': 1.88, 'max': 2.0}
entry offset patient-side (σ·√5): {'count': 34.0, 'mean': 0.16, 'std': 0.19, 'min': -0.02, '25%': 0.0, '50%': 0.05, '75%': 0.28, 'max': 0.64}
timeframe H: {'count': 77.0, 'mean': 11.9, 'std': 2.7, 'min': 10.0, '25%': 10.0, '50%': 10.0, '75%': 15.0, 'max': 20.0}
by era outcomes: {'new': {'expired': 15, 'open': 8, 'stop': 5, 'target': 1, 'unfilled': 8}, 'old': {'expired': 27, 'open': 1, 'stop': 1, 'target': 1, 'unfilled': 10}}
stop_u by era: {'new': 1.14, 'old': 1.29} tgt_u by era: {'new': 2.15, 'old': 2.11}
unfilled vs entry_off (median): {False: 0.01, True: 0.32}