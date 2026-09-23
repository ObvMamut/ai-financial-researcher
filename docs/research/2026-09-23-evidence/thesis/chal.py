from load import *
import collections,json
recs=[]
for d in runs():
    run=os.path.basename(d)
    for t,fs in items(d).items():
        for stage in ('challenge','challenge-final'):
            p=fs.get(stage+'-schema-repair') or fs.get(stage)
            if not p: continue
            j=parse(p)
            if j is None and fs.get(stage): j=parse(fs[stage])
            if not j: continue
            # matching dossier
            if stage=='challenge':
                rounds=sorted([k for k in fs if re.match(r'round-\d+$',k)])
                dk=None
                for k in rounds[::-1]:
                    for kk in (k+'-schema-repair',k+'-compacted',k):
                        if kk in fs and parse(fs[kk]): dk=kk;break
                    if dk: break
            else:
                dk=next((k for k in ('revision-schema-repair','revision-compacted','revision') if k in fs and parse(fs[k])),None)
            dos=parse(fs[dk]) if dk else None
            recs.append(dict(run=run,ticker=t,stage=stage,dossier_key=dk,ch=j,dos=dos))
json.dump([{k:v for k,v in r.items()} for r in recs],open('chal_recs.json','w'))
print(len(recs),collections.Counter((r['stage'],r['ch'].get('verdict')) for r in recs))
print('no dossier',sum(1 for r in recs if not r['dos']))
