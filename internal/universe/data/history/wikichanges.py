"""Parse the changes table of Wikipedia's "Historical components of the S&P 500"
(raw wikitext, action=raw) into JSON rows {date, added, removed, reason}.

usage: python3 -I wikichanges.py hist.wiki > changes.json
"""
import re,sys,json,datetime
src=open(sys.argv[1],encoding='utf-8').read()
tbl=src[src.index('id="changes"'):]
tbl=tbl[:tbl.index('\n|}')]
rows=tbl.split('\n|-')[2:]
def clean(c):
    c=re.sub(r'<ref[^>]*/>','',c); c=re.sub(r'<ref.*?</ref>','',c,flags=re.S); c=re.sub(r'<!--.*?-->','',c,flags=re.S)
    c=re.sub(r'\{\{[^|}]*\|([^}]*)\}\}',r'\1',c)
    c=re.sub(r'\[\[(?:[^|\]]*\|)?([^\]]*)\]\]',r'\1',c)
    return c.strip()
out=[];bad=[]
for r in rows:
    # A cell starts a line with '||' or '|', or follows another inline after '||'.
    cells=[clean(x) for x in re.split(r'\n\|\|?|\|\|',r)[1:]]
    if len(cells)<5: bad.append(r[:80]); continue
    try: d=datetime.datetime.strptime(cells[0].strip(),'%B %d, %Y').date().isoformat()
    except Exception: bad.append(cells[0]); continue
    out.append({'date':d,'added':cells[1].split()[0] if cells[1] else '','removed':cells[3].split()[0] if cells[3] else '','reason':cells[5] if len(cells)>5 else ''})
json.dump({'changes':out,'bad':bad},sys.stdout)
