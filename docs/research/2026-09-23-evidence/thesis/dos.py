from load import *
import json
out=[]
for d in runs():
    run=os.path.basename(d)
    for t,fs in items(d).items():
        order=['revision-schema-repair','revision-compacted','revision']+[f'round-{n}{s}' for n in (3,2,1) for s in ('-schema-repair','-compacted','')]
        for k in order:
            if k in fs:
                j=parse(fs[k])
                if j and j.get('long_case') is not None:
                    out.append(dict(run=run,ticker=t,key=k,dir=j.get('preferred_direction'),status=j.get('status'),
                      hyp=j.get('hypothesis',''),long=j.get('long_case',''),short=j.get('short_case',''),nt=j.get('no_trade_case',''),mech=j.get('mechanism','')))
                    break
json.dump(out,open('final_dossiers.json','w'),indent=1)
print(len(out))
import collections; print(collections.Counter(o['dir'] for o in out))
