"""Measure the current Go game Host builds in an isolated project-local source copy."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time

project = Path(__file__).resolve().parents[1] / 'go-server'


def measure(go):
    work = project / '.work'
    work.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='build-measure-',dir=work) as folder:
        root = Path(folder)
        src = root / 'source'
        src.mkdir()
        for directory in ('internal','cmd'):
            shutil.copytree(project/directory,src/directory)
        for file in ('go.mod','go.sum'):
            shutil.copyfile(project/file,src/file)
        tmp = root/'tmp'
        tmp.mkdir()
        env = {**os.environ,'GOCACHE':str(root/'cache'),'GOMODCACHE':str(work/'modules'),
               'GOPATH':str(work/'gopath'),'GOTMPDIR':str(tmp),'TMP':str(tmp),'TEMP':str(tmp),
               'GOTOOLCHAIN':'local','GOTELEMETRY':'off'}
        def build():
            begin = time.perf_counter()
            subprocess.run([go,'build','-o',root/'serve100.exe','./cmd/serve100'],cwd=src,env=env,check=True)
            return round(time.perf_counter()-begin,3)
        version = subprocess.check_output([go,'version'],cwd=src,env=env,text=True).strip()
        result = {'command':'go build -o serve100.exe ./cmd/serve100','go':version,'coldCompilerCacheSeconds':build(),
                  'noChangeSeconds':build()}
        source = src/'internal/host/game.go'
        code = source.read_bytes()
        old = b'invalid exit reason'
        if code.count(old)!=1:
            raise ValueError('game handler benchmark edit no longer matches')
        source.write_bytes(code.replace(old,b'invalid replay exit reason'))
        result['oneGameplayChangeSeconds'] = build()
        return result


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--go',required=True,type=Path)
    parser.add_argument('--output',type=Path)
    args=parser.parse_args()
    result=measure(args.go.resolve(strict=True))
    report=json.dumps(result,indent=2)
    if args.output:
        args.output.parent.mkdir(parents=True,exist_ok=True)
        args.output.write_text(report+'\n',encoding='utf8')
    print(report)
