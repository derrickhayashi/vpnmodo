# This fixed program executes on the VM. Operator values arrive only in a bounded
# JSON stdin header; byte reads preserve the following SSH stream without buffering.
import json, os, re, signal, subprocess, sys, time

def main():
    header = bytearray()
    while len(header) < 4096:
        b = os.read(0, 1)
        if b == b'\n': break
        if not b: raise ValueError()
        header.extend(b)
    else: raise ValueError()
    p = json.loads(header)
    if set(p) != {'profile','host','port'}: raise ValueError()
    if not isinstance(p['profile'],str) or not re.fullmatch(r'[a-z][a-z0-9-]{0,47}',p['profile']): raise ValueError()
    if not isinstance(p['host'],str) or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9.-]{0,252}',p['host']): raise ValueError()
    if type(p['port']) is not int or not 1 <= p['port'] <= 65535: raise ValueError()
    docker = ['docker','--host','unix:///var/run/docker.sock']
    bridge = docker + ['exec','-i','vpnmodo-controller','/usr/local/bin/vpnmodo-controller','bridge']
    frames = [dict(jsonrpc='2.0',id='hello',method='system.hello',params={'api':{'major':1,'minor':0}}),dict(jsonrpc='2.0',id='application',method='connection.application',params={'profile':p['profile']})]
    deadline=time.monotonic()+20
    cid=None
    signal.signal(signal.SIGALRM,lambda *_: (_ for _ in ()).throw(TimeoutError()))
    signal.alarm(25)
    while cid is None:
        child=subprocess.Popen(bridge,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL)
        try:
            child.stdin.write(json.dumps(frames[0],separators=(',',':')).encode()+b'\n');child.stdin.flush()
            hello=json.loads(child.stdout.readline(1048577))
            if hello.get('id')!='hello' or hello.get('result',{}).get('api',{}).get('major')!=1:raise ValueError()
            child.stdin.write(json.dumps(frames[1],separators=(',',':')).encode()+b'\n');child.stdin.close()
            result=json.loads(child.stdout.readline(1048577))
            if child.stdout.read(1) or child.wait(timeout=3)!=0:raise ValueError()
            if result.get('id')!='application':raise ValueError()
            if result.get('error',{}).get('code')==-32004 and time.monotonic()<deadline:
                time.sleep(.1)
                continue
            cid=result.get('result',{}).get('container_id')
            if not isinstance(cid,str) or not re.fullmatch(r'[0-9a-f]{64}',cid):raise ValueError()
        finally:
            if child.poll() is None:child.kill()
            child.wait()
    signal.alarm(0)
    # The stream runs in the verified application, independently of the controller
    # container. No SSH keys/agent enter the VM or the VPN/application containers.
    os.execvp('docker',docker+['exec','-i',cid,'nc',p['host'],str(p['port'])])
try:
    main()
except Exception:
    sys.stderr.write('managed service transport unavailable\n')
    sys.exit(1)
