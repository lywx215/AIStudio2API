"""Reproducible new-VPS deployment. Passwords are never CLI arguments or reports.

This deliberately refuses to overwrite an existing unrelated installation.
The root-owned remote job survives a dropped SSH connection; status is resumable.
"""
from __future__ import annotations

import argparse
import base64
import datetime as dt
import getpass
import hashlib
import importlib.util
import ipaddress
import json
import logging
import pathlib
import re
import shlex
import socket
import subprocess
import sys
import time
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[1]
RUNTIME = ROOT / 'runtime' / 'vps-deploy'
REMOTE = '/opt/aistudio2api'
sys.path.insert(0, str(RUNTIME / 'tools'))


def emit(event, **fields):
    print(json.dumps({'event': event, **fields}, ensure_ascii=False), flush=True)


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def load_defaults():
    helper = pathlib.Path.home() / '.codex/private/aistudio2api-vps/credentials.py'
    spec = importlib.util.spec_from_file_location('aistudio_vps_defaults', helper)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    env = module.load_defaults()['environment']
    env['PORT'] = env['WEB_PORT'] = '2048'
    if any('\n' in str(v) or '\r' in str(v) for v in env.values()):
        raise ValueError('Multiline environment values are unsupported')
    if any(not re.fullmatch(r'[A-Z][A-Z0-9_]*', k) for k in env):
        raise ValueError('Invalid environment field name')
    return env


def connect(args, password):
    import paramiko
    logging.getLogger('paramiko').setLevel(logging.CRITICAL)
    ipaddress.IPv4Address(args.host)
    client = paramiko.SSHClient()
    for path in [pathlib.Path.home() / '.ssh/known_hosts', RUNTIME / 'known_hosts']:
        if path.exists():
            client.load_host_keys(str(path))
    client.set_missing_host_key_policy(paramiko.RejectPolicy())
    tunnel = None
    if not args.direct:
        tunnel = socket.create_connection(('127.0.0.1', 7897), timeout=15)
        tunnel.sendall(('CONNECT ' + args.host + ':22 HTTP/1.1\r\nHost: ' + args.host + ':22\r\n\r\n').encode())
        header = b''
        while b'\r\n\r\n' not in header and len(header) < 8192:
            byte = tunnel.recv(1)
            if not byte:
                raise ConnectionError('Proxy CONNECT closed early')
            header += byte
        if header.split(b'\r\n', 1)[0].split()[1] != b'200':
            raise ConnectionError('Proxy CONNECT was rejected')
    try:
        client.connect(args.host, username='root', password=password, sock=tunnel,
                       timeout=20, banner_timeout=20, auth_timeout=20,
                       look_for_keys=False, allow_agent=False)
        key = client.get_transport().get_remote_server_key()
        fingerprint = 'SHA256:' + base64.b64encode(hashlib.sha256(key.asbytes()).digest()).decode().rstrip('=')
        emit('ssh_connected', host=args.host, host_key_pinned=True, fingerprint=fingerprint)
        return client
    except BaseException:
        client.close()
        if tunnel:
            tunnel.close()
        raise


def remote(client, script, payload=None, timeout=45):
    stdin, stdout, stderr = client.exec_command('python3 -c ' + shlex.quote(script), timeout=timeout)
    if payload is not None:
        stdin.write(json.dumps(payload))
        stdin.flush()
    stdin.channel.shutdown_write()
    result = stdout.read().decode('utf-8', 'replace')
    # Do not forward arbitrary stderr: it can come from commands reading protected config.
    stderr.read()
    code = stdout.channel.recv_exit_status()
    if code:
        raise RuntimeError('Remote step failed (exit ' + str(code) + '); inspect root-only deployment logs')
    return json.loads(result)


def upload(client, path, content, mode=0o600):
    with client.open_sftp() as sftp:
        with sftp.file(path, 'wb') as stream:
            stream.write(content)
        sftp.chmod(path, mode)


def compose(image):
    # Compose >=2.30: raw format preserves literal dollars and quotes in secrets.
    return f'''services:
  aistudio2api:
    image: {image}
    container_name: aistudio2api
    restart: unless-stopped
    init: true
    shm_size: 1gb
    ports:
      - "2048:2048"
    env_file:
      - path: .env
        format: raw
    volumes:
      - ./data:/data
    logging:
      driver: json-file
      options:
        max-size: 10m
        max-file: "3"
'''


PREFLIGHT = r'''
import json,os,pathlib,shutil,socket,subprocess,sys
cfg=json.load(sys.stdin);base=pathlib.Path('/opt/aistudio2api')
release=dict(line.split('=',1) for line in pathlib.Path('/etc/os-release').read_text().splitlines() if '=' in line)
release={k:v.strip('"') for k,v in release.items()}
assert os.geteuid()==0,'Root SSH is required'
assert release['ID']=='debian' and release['VERSION_ID'] in {'12','13'},'Supported hosts: Debian 12/13'
m={line.split(':')[0]:int(line.split()[1])*1024 for line in pathlib.Path('/proc/meminfo').read_text().splitlines()}
assert m['MemTotal']>=8*1024**3,'This browser deployment requires at least 8 GiB actual RAM'
assert shutil.disk_usage('/opt').free>=12*1024**3,'At least 12 GiB free disk is required'
manifest=base/'deployment-manifest.json'
if base.exists():
 assert cfg['resume'] and manifest.is_file(),'Existing application refused; use its separate update/backup procedure'
 old=json.loads(manifest.read_text())
 assert all(old.get(k)==cfg[k] for k in ['commit','archive_sha256','image']),'Resume manifest mismatch'
else:
 s=socket.socket()
 try:s.bind(('0.0.0.0',2048))
 finally:s.close()
 if shutil.which('docker'):
  p=subprocess.run(['docker','container','inspect','aistudio2api'],capture_output=True)
  assert p.returncode!=0,'Existing aistudio2api container refused'
 base.mkdir(mode=0o700)
 (base/'data').mkdir(mode=0o700)
 (base/'data/auth').mkdir(mode=0o700)
 manifest.write_text(json.dumps({k:cfg[k] for k in ['commit','archive_sha256','image','branch']},indent=2)+'\n')
 manifest.chmod(0o600)
print(json.dumps({'os':release['PRETTY_NAME'],'architecture':subprocess.check_output(['dpkg','--print-architecture'],text=True).strip(),'cpus':os.cpu_count(),'memory_total_bytes':m['MemTotal'],'memory_available_bytes':m['MemAvailable'],'disk_free_bytes':shutil.disk_usage('/opt').free,'resume':cfg['resume']}))
'''


JOB = r'''
import datetime,fcntl,hashlib,json,os,pathlib,shutil,subprocess,tarfile,time,urllib.request
base=pathlib.Path('/opt/aistudio2api')
cfg=json.loads((base/'deployment-manifest.json').read_text())
lock=(base/'deployment.lock').open('w');fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
state={'commit':cfg['commit'],'branch':cfg['branch'],'image':cfg['image'],'started_at':datetime.datetime.now(datetime.timezone.utc).isoformat()}
def stage(name,**fields):
 state.update(stage=name,updated_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),**fields)
 path=base/'deployment-state.json';temporary=base/'deployment-state.json.tmp'
 temporary.write_text(json.dumps(state,indent=2)+'\n');temporary.chmod(0o600);temporary.replace(path)
def run(command,timeout=1200):
 with (base/'deployment-build.log').open('ab') as log:
  os.chmod(base/'deployment-build.log',0o600)
  p=subprocess.run(command,cwd=base,stdout=log,stderr=subprocess.STDOUT,timeout=timeout)
 if p.returncode:raise RuntimeError('Deployment command failed; inspect /opt/aistudio2api/deployment-build.log')
try:
 stage('install_docker')
 release=dict(line.split('=',1) for line in pathlib.Path('/etc/os-release').read_text().splitlines() if '=' in line)
 codename=release['VERSION_CODENAME'].strip('"')
 arch=subprocess.check_output(['dpkg','--print-architecture'],text=True).strip()
 assert arch in {'amd64','arm64'},'Unsupported architecture'
 os.environ['DEBIAN_FRONTEND']='noninteractive'
 if not shutil.which('docker'):
  run(['apt-get','update','-qq'])
  run(['apt-get','install','-y','-qq','ca-certificates','curl'])
  pathlib.Path('/etc/apt/keyrings').mkdir(mode=0o755,exist_ok=True)
  run(['curl','--proto','=https','--tlsv1.2','--fail','--silent','--show-error','https://download.docker.com/linux/debian/gpg','-o','/etc/apt/keyrings/docker.asc'],120)
  os.chmod('/etc/apt/keyrings/docker.asc',0o644)
  pathlib.Path('/etc/apt/sources.list.d/docker.sources').write_text('Types: deb\nURIs: https://download.docker.com/linux/debian\nSuites: '+codename+'\nComponents: stable\nArchitectures: '+arch+'\nSigned-By: /etc/apt/keyrings/docker.asc\n')
  run(['apt-get','update','-qq'])
  run(['apt-get','install','-y','-qq','docker-ce','docker-ce-cli','containerd.io','docker-buildx-plugin','docker-compose-plugin'])
 run(['systemctl','enable','--now','docker'],120)
 docker_version=subprocess.check_output(['docker','version','--format','{{.Server.Version}}'],text=True).strip()
 compose_version=subprocess.check_output(['docker','compose','version','--short'],text=True).strip()
 version=compose_version.lstrip('v').split('.')
 assert (int(version[0]),int(version[1]))>=(2,30),'Compose >=2.30 is required for raw env_file'
 stage('unpack_source',docker_version=docker_version,compose_version=compose_version)
 archive=base/'source.tar';expected=cfg['archive_sha256']
 assert hashlib.sha256(archive.read_bytes()).hexdigest()==expected,'Source archive digest mismatch'
 source=base/'source'/cfg['commit'];source.mkdir(parents=True,exist_ok=True)
 with tarfile.open(archive) as stream:stream.extractall(source,filter='data')
 stage('build_image',source_archive_sha256_verified=True)
 existing=subprocess.run(['docker','image','inspect',cfg['image']],capture_output=True,text=True)
 reuse=False
 if existing.returncode==0:
  image=json.loads(existing.stdout)[0]
  reuse=image['Config'].get('Labels',{}).get('org.opencontainers.image.revision')==cfg['commit']
 if not reuse:
  run(['docker','build','--progress=plain','--build-arg','VERSION='+cfg['commit'][:12],
       '--build-arg','MANAGED_BY=lywx215','--label','org.opencontainers.image.revision='+cfg['commit'],
       '--label','io.aistudio2api.source-branch='+cfg['branch'],'-t',cfg['image'],str(source)],2400)
 stage('start_container',image_reused=reuse)
 run(['docker','compose','config','--quiet'],60)
 run(['docker','compose','up','-d','aistudio2api'],180)
 stage('health_check')
 opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
 for attempt in range(450):
  try:
   with opener.open('http://127.0.0.1:2048/health',timeout=3) as response:
    if response.status==200 and json.load(response).get('status')=='ok':break
  except Exception:pass
  time.sleep(2)
 else:raise RuntimeError('Application health check timed out')
 c=json.loads(subprocess.check_output(['docker','inspect','aistudio2api'],text=True))[0]
 assert c['Config']['Image']==cfg['image']
 assert c['Config']['Labels']['org.opencontainers.image.revision']==cfg['commit']
 assert c['HostConfig']['RestartPolicy']['Name']=='unless-stopped'
 assert any(m.get('Source')==str(base/'data') and m.get('Destination')=='/data' for m in c['Mounts'])
 stage('complete',deployed=True,health_http=200,health_status='ok',container_state=c['State']['Status'],restart_policy='unless-stopped',persistent_data_mount_verified=True,oom_killed=c['State']['OOMKilled'])
except BaseException as error:
 stage('failed',deployed=False,failure_type=type(error).__name__,failure_stage=state.get('stage'),
       failure_reason=str(error) if isinstance(error,(AssertionError,RuntimeError)) else 'See root-only build log for command diagnostics')
 raise
'''


STATUS = r'''
import json,pathlib,subprocess
base=pathlib.Path('/opt/aistudio2api');path=base/'deployment-state.json'
state=json.loads(path.read_text()) if path.exists() else {'stage':'not_started'}
state['job_active']=subprocess.run(['systemctl','is-active','--quiet','aistudio2api-deploy.service']).returncode==0
log=base/'deployment-build.log'
if log.exists():
 state['build_log_bytes']=log.stat().st_size
 # This log is exclusively package installation and immutable-source builds;
 # application logs and environment files are never included.
 with log.open('rb') as stream:
  stream.seek(max(0,log.stat().st_size-4096))
  state['build_tail']=stream.read().decode('utf-8','replace').splitlines()[-8:]
print(json.dumps(state))
'''


VERIFY = r'''
import http.cookiejar,json,pathlib,stat,subprocess,sys,urllib.request,urllib.error
env=json.load(sys.stdin);origin='http://127.0.0.1:2048'
class NoRedirect(urllib.request.HTTPRedirectHandler):
 def redirect_request(self,*args,**kwargs):return None
jar=http.cookiejar.CookieJar();opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect(),urllib.request.HTTPCookieProcessor(jar))
def request(path,body=None,model=False):
 headers={'Content-Type':'application/json','Origin':origin}
 if model:headers['Authorization']='Bearer '+env['PROXY_API_KEY']
 req=urllib.request.Request(origin+path,headers=headers,data=json.dumps(body).encode() if body is not None else None)
 try:response=opener.open(req,timeout=20)
 except urllib.error.HTTPError as error:response=error
 with response:
  raw=response.read()
  return response.status,json.loads(raw) if raw else {}
logged_in=False
try:
 code,data=request('/api/auth/login',{'username':env['ADMIN_USERNAME'],'password':env['ADMIN_PASSWORD']})
 assert code==200 and data.get('authenticated'),'Admin authentication failed'
 logged_in=True
 _,status=request('/api/status');_,accounts=request('/api/accounts')
 auth_code,catalogue=request('/v1/models',model=True);no_auth,_=request('/v1/models')
 assert no_auth==401,'Unauthenticated model catalogue should be rejected'
 error=catalogue.get('error',{})
 assert auth_code==200 or auth_code==503 and error.get('code') in {'service_stopped','service_starting','accounts_not_ready','no_accounts'},'Model-key check failed'
 assert stat.S_IMODE(pathlib.Path('/opt/aistudio2api/.env').stat().st_mode)==0o600
 container=json.loads(subprocess.check_output(['docker','inspect','aistudio2api'],text=True))[0]
 actual=dict(item.split('=',1) for item in container['Config']['Env'] if '=' in item)
 assert all(actual.get(k)==str(v) for k,v in env.items()),'Container environment differs from saved defaults'
 print(json.dumps({'admin_login_http':code,'model_catalogue_authenticated_http':auth_code,'model_catalogue_error_code':error.get('code'),'model_catalogue_without_key_http':no_auth,'account_count':len(accounts.get('accounts',[])),'generation_service_state':status.get('state'),'protected_env_permissions':'0600','environment_matches_saved_defaults':True,'application_timezone':actual.get('TZ'),'model_generation_calls':0}))
finally:
 if logged_in:request('/api/auth/logout',{})
 env.clear()
'''


HTTPS = r'''
import datetime,json,os,pathlib,shutil,socket,subprocess,sys
cfg=json.load(sys.stdin);domain=cfg['domain'];base=pathlib.Path('/opt/aistudio2api')
addresses={v[4][0] for v in socket.getaddrinfo(domain,443,socket.AF_INET)}
assert cfg['host'] in addresses,'Domain does not resolve to this server'
log=base/'https-setup.log'
def run(command,timeout=240):
 with log.open('ab') as stream:
  log.chmod(0o600)
  p=subprocess.run(command,stdout=stream,stderr=subprocess.STDOUT,timeout=timeout)
 if p.returncode:raise RuntimeError('HTTPS command failed; inspect root-only https-setup.log')
installed_now=not shutil.which('caddy')
if installed_now:
 os.environ['DEBIAN_FRONTEND']='noninteractive'
 run(['apt-get','install','-y','-qq','debian-keyring','debian-archive-keyring','apt-transport-https','curl','gnupg'])
 run(['curl','--proto','=https','--tlsv1.2','-fsSL','https://dl.cloudsmith.io/public/caddy/stable/gpg.key','-o',str(base/'caddy-signing-key.asc')],90)
 run(['gpg','--batch','--yes','--dearmor','-o','/usr/share/keyrings/caddy-stable-archive-keyring.gpg',str(base/'caddy-signing-key.asc')])
 run(['curl','--proto','=https','--tlsv1.2','-fsSL','https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt','-o','/etc/apt/sources.list.d/caddy-stable.list'],90)
 os.chmod('/usr/share/keyrings/caddy-stable-archive-keyring.gpg',0o644)
 os.chmod('/etc/apt/sources.list.d/caddy-stable.list',0o644)
 run(['apt-get','update','-qq'])
 run(['apt-get','install','-y','-qq','caddy'])
path=pathlib.Path('/etc/caddy/Caddyfile');original=path.read_bytes()
text=original.decode();backup=path.with_name('Caddyfile.before-'+domain.split('.')[0]+'-'+datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ'))
backup.write_bytes(original);backup.chmod(0o600)
block=domain+' {\n    reverse_proxy 127.0.0.1:2048\n}\n'
if installed_now:text=block
elif domain not in text:text=text.rstrip()+'\n\n'+block
pending=path.with_name('Caddyfile.pending-aistudio2api');pending.write_text(text);pending.chmod(0o644)
run(['caddy','fmt','--overwrite',str(pending)],30)
run(['caddy','validate','--config',str(pending),'--adapter','caddyfile'],30)
os.replace(pending,path)
try:
 run(['systemctl','enable','caddy'],60)
 run(['systemctl','reload-or-restart','caddy'],60)
except BaseException:
 path.write_bytes(original)
 subprocess.run(['systemctl','reload-or-restart','caddy'],capture_output=True,timeout=60)
 raise
active=subprocess.check_output(['systemctl','is-active','caddy'],text=True).strip()
assert active=='active','Caddy did not become active'
print(json.dumps({'domain':domain,'dns_ipv4':sorted(addresses),'caddy_active':active,'caddy_version':subprocess.check_output(['caddy','version'],text=True).strip(),'backup':str(backup),'upstream':'127.0.0.1:2048','application_restarted':False}))
'''


def https_checks(domain, direct):
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args, **kwargs):
            return None
    from urllib.error import HTTPError
    proxies = {} if direct else {'http': 'http://127.0.0.1:7897', 'https': 'http://127.0.0.1:7897'}
    opener = urllib.request.build_opener(urllib.request.ProxyHandler(proxies), NoRedirect())
    result = {'origin': 'https://' + domain, 'tls_verification': 'strict'}
    for label, path in [('homepage', '/'), ('health', '/health')]:
        with opener.open(result['origin'] + path, timeout=20) as response:
            result[label + '_http'] = response.status
            if label == 'health':
                result['health_status'] = json.load(response).get('status')
    try:
        with opener.open('http://' + domain + '/', timeout=20) as response:
            result['http_redirect_status'] = response.status
    except HTTPError as error:
        result['http_redirect_status'] = error.code
        result['http_redirect_location'] = error.headers.get('Location')
        error.close()
    if result.get('http_redirect_status') not in {301, 302, 307, 308} or result.get('http_redirect_location') != result['origin'] + '/':
        raise RuntimeError('HTTP did not redirect to the expected HTTPS origin')
    return result


def public_checks(host, direct):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({} if direct else {'http': 'http://127.0.0.1:7897'}))
    result = {}
    for label, path in [('homepage', '/'), ('health', '/health')]:
        with opener.open('http://' + host + ':2048' + path, timeout=20) as response:
            result[label + '_http'] = response.status
            if label == 'health':
                result['health_status'] = json.load(response).get('status')
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['deploy', 'status', 'https'])
    parser.add_argument('--host', required=True)
    parser.add_argument('--commit')
    parser.add_argument('--branch', choices=['main', 'dev'], default='dev')
    parser.add_argument('--resume', action='store_true')
    parser.add_argument('--direct', action='store_true', help='Do not use the local HTTP CONNECT proxy at 127.0.0.1:7897')
    parser.add_argument('--domain', help='Optional public DNS name for Caddy HTTPS')
    args = parser.parse_args()
    ipaddress.IPv4Address(args.host)
    if args.domain and (len(args.domain) > 253 or not re.fullmatch(r'(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,63}', args.domain)):
        parser.error('Invalid public domain name')
    if args.action == 'https' and not args.domain:
        parser.error('https requires --domain')
    if args.action == 'deploy' and not re.fullmatch('[0-9a-f]{40}', args.commit or ''):
        parser.error('deploy requires a full --commit SHA')
    RUNTIME.mkdir(parents=True, exist_ok=True)
    env = None
    client = None
    password = getpass.getpass('SSH root password (hidden): ')
    record = {'host': args.host, 'url': 'http://' + args.host + ':2048', 'started_at': now(), 'model_generation_calls': 0}
    report = RUNTIME / ('deployment-' + args.host + '-latest.json')
    if report.exists() and args.action in {'status', 'https'}:
        original = json.loads(report.read_text(encoding='utf-8'))
        record = {**original, **record}
        record['last_checked_at'] = now()
    try:
        client = connect(args, password)
        if args.action == 'deploy':
            resolved = subprocess.check_output(['git', 'rev-parse', '--verify', args.commit + '^{commit}'], cwd=ROOT, text=True).strip()
            assert resolved == args.commit
            archive = RUNTIME / ('source-' + args.commit[:12] + '.tar')
            subprocess.run(['git', 'archive', '--format=tar', '--output=' + str(archive), args.commit], cwd=ROOT, check=True)
            image = 'aistudio2api:' + args.commit[:12]
            config = {'commit': args.commit, 'archive_sha256': hashlib.sha256(archive.read_bytes()).hexdigest(), 'image': image, 'branch': args.branch, 'resume': args.resume}
            record.update(commit=args.commit, source_branch=args.branch, image=image, source_archive_sha256=config['archive_sha256'])
            record['preflight'] = remote(client, PREFLIGHT, config)
            emit('preflight_passed', **record['preflight'])
            current = remote(client, STATUS)
            if current.get('stage') != 'complete' and not current.get('job_active'):
                env = load_defaults()
                check_existing = "import hashlib,json,pathlib,sys;cfg=json.load(sys.stdin);p=pathlib.Path('/opt/aistudio2api/.env');print(json.dumps({'matches':not p.exists() or p.read_bytes()==cfg['content'].encode()}))"
                content = ''.join(k + '=' + str(v) + '\n' for k, v in env.items())
                if not remote(client, check_existing, {'content': content})['matches']:
                    raise RuntimeError('Existing application environment differs; refused to overwrite')
                upload(client, REMOTE + '/.env', content.encode())
                upload(client, REMOTE + '/compose.yaml', compose(image).encode(), 0o644)
                with client.open_sftp() as sftp:
                    sftp.put(str(archive), REMOTE + '/source.tar')
                    sftp.chmod(REMOTE + '/source.tar', 0o600)
                upload(client, REMOTE + '/deploy-job.py', JOB.encode())
                start = "import json,subprocess;subprocess.run(['systemctl','reset-failed','aistudio2api-deploy.service'],capture_output=True);subprocess.run(['systemd-run','--unit=aistudio2api-deploy','--collect','--property=RuntimeMaxSec=4200','--property=UMask=0077','/usr/bin/python3','/opt/aistudio2api/deploy-job.py'],check=True,capture_output=True);print(json.dumps({'started':True}))"
                remote(client, start)
                emit('deployment_job_started', survives_ssh_disconnect=True)
            deadline = time.monotonic() + 4200
            previous = None
            while True:
                state = remote(client, STATUS)
                if state.get('stage') != previous:
                    emit('deployment_progress', **state)
                    previous = state.get('stage')
                else:
                    emit('deployment_running', deployment_stage=state.get('stage'))
                record['deployment'] = state
                report.write_text(json.dumps(record, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
                if state.get('stage') == 'complete':
                    break
                if state.get('stage') == 'failed' or time.monotonic() > deadline:
                    raise RuntimeError('Deployment failed or exceeded deadline; inspect deployment-state.json and deployment-build.log')
                if not state.get('job_active') and state.get('stage') != 'not_started':
                    raise RuntimeError('Remote deployment job exited before completion')
                time.sleep(20)
            if env is None:
                env = load_defaults()
            record['authenticated_checks'] = remote(client, VERIFY, env, timeout=90)
            record['public_checks'] = public_checks(args.host, args.direct)
            record.update(deployed=True, verified_at=now())
        elif args.action == 'status':
            record['deployment'] = remote(client, STATUS)
            try:
                record['public_checks'] = public_checks(args.host, args.direct)
            except Exception as error:
                record['public_checks'] = {'available': False, 'error_type': type(error).__name__}
        if args.domain:
            if args.action in {'deploy', 'https'}:
                emit('configure_https', domain=args.domain)
                record['https_configuration'] = remote(client, HTTPS, {'domain': args.domain, 'host': args.host}, timeout=900)
            deadline = time.monotonic() + 180
            while True:
                try:
                    record['https_checks'] = https_checks(args.domain, args.direct)
                    break
                except Exception:
                    if time.monotonic() > deadline:
                        raise RuntimeError('HTTPS did not become healthy; check DNS, ingress and Caddy logs')
                    emit('waiting_for_https', domain=args.domain)
                    time.sleep(10)
            record['url'] = 'https://' + args.domain
            record['verified_at'] = now()
        report.write_text(json.dumps(record, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
        emit('complete', **record, report=str(report))
    except Exception as error:
        # Avoid forwarding exception objects produced by secret-bearing requests.
        record.update(deployed=False, failure_type=type(error).__name__, failed_at=now())
        if isinstance(error, RuntimeError):
            record['failure_reason'] = str(error)
        report.write_text(json.dumps(record, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
        emit('failed', failure_type=type(error).__name__, report=str(report))
        raise SystemExit(1)
    finally:
        if client:
            client.close()
        if env:
            env.clear()
        password = None


if __name__ == '__main__':
    main()
