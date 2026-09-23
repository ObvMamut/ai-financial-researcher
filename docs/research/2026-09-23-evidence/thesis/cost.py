from load import *
import json,collections,re
tot_stage=collections.Counter(); rows=[]
outc=collections.Counter(); contract=collections.Counter(); dec=collections.Counter(); review=collections.Counter()
for d in runs():
    try: m=json.load(open(d+'/metadata.json'))
    except Exception as e: print('nometa',d); continue
    if m.get('research_mode')!='thesis': print('notthesis',d,m.get('research_mode')); continue
    st=collections.Counter(); calls=0; tok=0; fails=0
    for x in m.get('domains',[]):
        name=x.get('domain','')
        if name.startswith('research-'):
            rest=re.sub(r'^research-[0-9a-f]+-','',name)
            if 'compaction' in rest: s='compaction'
            elif 'schema-repair' in rest: s='schema-repair'
            elif rest.startswith('challenge'): s='challenger'
            elif rest.startswith('revision'): s='revision'
            elif rest.startswith('round'): s='research-'+rest.split('-')[1] if rest.count('-')>=1 else 'research'
            else: s='research-other'
        elif name.startswith('event-discovery'): s='event-discovery'
        else: s=name
        u=x.get('usage') or []
        t=sum(v.get('total_tokens',0) for v in u)
        calls+=max(len(u),1 if x.get('status')!='not_run' else 0)
        st[s]+=t; tok+=t; tot_stage[s]+=t
        if x.get('status') not in ('done',): fails+=1
    ro=m.get('research_outcomes',[])
    ran=[r for r in ro if r.get('transport')!='not_run']
    for r in ro:
        outc[(r.get('transport'),r.get('parsing'))]+=1; contract[r.get('contract')]+=1; dec[r.get('decision')]+=1; review[r.get('review')]+=1
    nfailc=sum(1 for r in ran if r.get('contract') not in (None,'ok','compacted'))
    ncomp=sum(1 for r in ran if r.get('compaction_attempts',0)>0)
    nbad=sum(1 for r in ran if r.get('transport')!='ok' or r.get('parsing')!='ok')
    rows.append(dict(run=os.path.basename(d),tokens=tok,dur_s=round(m.get('total_duration_ms',0)/1000),calls=calls,domain_not_done=fails,
      researched=len(ran),shortlist=len(ro),compacted=ncomp,contract_other=nfailc,transport_or_parse_bad=nbad,
      chief=m.get('chief_model'),cheap=m.get('engine_model'),outcome=m.get('outcome'),ideas=None,top=st.most_common(3)))
for r in rows: print(r)
print('STAGE TOTALS',tot_stage.most_common())
print('outcomes',outc); print('contract',contract); print('decision',dec); print('review',review)
json.dump(rows,open('cost_rows.json','w'),indent=1)
