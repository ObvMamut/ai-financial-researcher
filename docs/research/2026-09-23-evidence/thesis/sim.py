import json,collections
from cat import label
R=json.load(open('chal_recs.json'))
U={'Earnings/event date or release time unverified (3rd-party calendar)','Analyst target / annual horizon vs 10-15 sessions','Future / post-event prices unavailable','Priced-in / expectations unprovable','Mechanism / catalyst / transmission in window unevidenced'}
# final per (run,ticker)
fin={}
for r in R:
    k=(r['run'],r['ticker'])
    if k not in fin or r['stage']=='challenge-final': fin[k]=r
def ev(r,att=False):
    ch=r['ch'];dos=r['dos'] or {}
    rv={c.get('claim_id'):c for c in ch.get('claim_reviews') or []}
    ok=lambda c: c and c.get('assessment')=='supported' and (not att or c.get('attribution')=='confirmed')
    dis=any(c.get('assessment')=='disputed' or (att and c.get('attribution')=='disputed') for c in rv.values())
    union=[]
    for i in (dos.get('expectations_claim_ids') or [])+(dos.get('priced_in_claim_ids') or []):
        if i not in union: union.append(i)
    claims=[c.get('id') for c in dos.get('claims') or []]
    first3=claims[:3]
    core_full=union or first3
    core3=(union or first3)[:3]
    has=bool(rv)
    return dict(has=has,
      nodisp=has and not dis,
      A_union= has and not dis and all(ok(rv.get(i)) for i in core_full),
      A_union3=has and not dis and all(ok(rv.get(i)) for i in core3),
      A_first3=has and not dis and all(ok(rv.get(i)) for i in first3),
      core_n=len(union), dir=dos.get('preferred_direction'))
for name,S in (('all challenges',R),('final per run-ticker',list(fin.values()))):
  for att in (False,True):
    c=collections.Counter(); dirs=collections.Counter()
    for r in S:
        e=ev(r,att)
        for k in ('has','nodisp','A_union','A_union3','A_first3'): c[k]+=e[k]
        if e['A_union3']: dirs[e['dir']]+=1
    print(name,'attr-strict' if att else 'assessment-only',len(S),dict(c),'A_union3 dirs',dict(dirs))
# union size dist
print('union size',collections.Counter(ev(r)['core_n'] for r in fin.values()))
# per-challenge issue category presence
pres=collections.Counter(); allU=0; n=0; anyU=0
for r in fin.values():
    mi=[x for x in r['ch'].get('material_issues') or [] if isinstance(x,str)]
    if not mi: continue
    n+=1
    labs=[label(x)[0] for x in mi]
    for l in set(labs): pres[l]+=1
    if all(l in U for l in labs): allU+=1
    if any(l in U for l in labs): anyU+=1
print('final challenges w/ issues',n,'all-unsatisfiable',allU,'any-unsat',anyU)
for k,v in pres.most_common(): print(v,k)
out=[]
for r in fin.values():
    e=ev(r); out.append(dict(run=r['run'],ticker=r['ticker'],stage=r['stage'],verdict=r['ch'].get('verdict'),**e))
json.dump(out,open('sim_final.json','w'),indent=0)
