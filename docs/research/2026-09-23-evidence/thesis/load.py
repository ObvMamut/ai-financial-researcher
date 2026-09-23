import json,os,re,glob,collections
R='/home/mamut/all/programming/claude-financial-researcher/runs'
def parse(path):
    t=open(path,errors='replace').read()
    m=re.findall(r'```json\s*(.*?)```',t,re.S)
    cands=m[::-1]+[t]
    for c in cands:
        c=c.strip()
        try: return json.loads(strict=False,s=c)
        except Exception:
            i=c.find('{'); j=c.rfind('}')
            if i>=0:
                try: return json.loads(strict=False,s=c[i:j+1])
                except Exception: pass
    return None
def runs():
    out=[]
    for d in sorted(glob.glob(R+'/2026-09-*')):
        if glob.glob(d+'/research-*'): out.append(d)
    return out
def items(d):
    by=collections.defaultdict(dict)
    for f in os.listdir(d):
        m=re.match(r'research-([0-9a-f]+)-(.*)\.md$',f)
        if m: by[bytes.fromhex(m.group(1)).decode(errors='replace')][m.group(2)]=os.path.join(d,f)
    return by
