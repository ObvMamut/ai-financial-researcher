import re,collections,json,sys
CATS=[
('TA/chart levels used',r'chart|resistance|support level|moving.average|technical analysis|\bsupport ~|support/resist'),
('Pipeline artifact (tool strings, compaction, schema, hash)',r'tool-(generated|emitted)|formatting|compaction|compacted|dossier hash|schema|unknown or repeated|did not address claim'),
('Execution plan / target_claim_ids / entry-condition form',r'plan hash|target_claim_ids|execution plan|target_assessment|entry condition|entry_condition|monitoring'),
('Earnings/event date or release time unverified (3rd-party calendar)',r'earnings (date|time|call|release|calendar)|release time|event time|announcement time|earnings calendar|third-party calendar|calendar (entry|date|value)|events calendar|alphavantage|report(s|ing)? date|next (print|report)|scheduled event|no event|event exposure|confirmed date|issuer-stated|release date|results date|investor day'),
('Analyst target / annual horizon vs 10-15 sessions',r'price target|analyst target|\btargets\b|target price|12-month|twelve-month|annual target|price objective|upside|price forecast|eur \d+ target|\$[\d.]+ target'),
('Future / post-event prices unavailable',r'post-event|post-\d{4}|no post|awaiting|awaiting-prices|future observation|no price (data|session)|unelapsed|has not (yet )?elapsed|not yet'),
('Quote/passage/attribution grounding',r'passage|quot|verbatim|issuer role|issuer.s role|attribution|ungrounded|not (found|present) in|does not (state|contain|appear)|no such|misattribut|who acted'),
('Source stale / secondary / inaccessible',r'stale|secondary|third-party|aggregator|truncated|http 40|403|401|primary|summary|opinion|seeking alpha|zacks|\bold\b|duplicate|same event|one underlying|recycl|not independent'),
('Positioning/insider/flow misread',r'insider|form 4|form 144|10b5|put/call|13f|positioning|option|short interest|options? flow|fund flow|inflow'),
('Priced-in / expectations unprovable',r'priced|incorporat|expectation|consensus|discrepan|discount|baseline|surprise'),
('Mechanism / catalyst / transmission in window unevidenced',r'mechanism|transmission|catalyst|within the window|10.15 session|in-window|inside the window|near-term test|causal|driver'),
('Direction / opposite case / no-trade reasoning',r'short case|long case|opposite|no-trade|no trade|preferred.direction|direction|\bnone\b|stand aside'),
('Unanswered/unrequested retrieval',r'request|retriev|fetch|not obtained|should (open|read)'),
]
def label(s):
    s2=s.lower()
    hits=[n for n,p in CATS if re.search(p,s2)]
    return hits[0] if hits else 'Other', hits
if __name__=='__main__':
    for fn in ('mi.txt','cr.txt'):
        L=[l for l in open(fn).read().split('\n') if l.strip()]
        prim=collections.Counter(); multi=collections.Counter(); ex=collections.defaultdict(list)
        for l in L:
            p,h=label(l); prim[p]+=1
            for x in h: multi[x]+=1
            ex[p].append(l)
        print('==',fn,len(L))
        for n,_ in CATS+[('Other','')]: print(f'{prim[n]:4d} primary {multi[n]:4d} any  {n}')
        json.dump(ex,open(fn+'.cats.json','w'),indent=1)
