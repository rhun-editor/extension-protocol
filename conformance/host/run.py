#!/usr/bin/env python3
"""Host conformance suite for the rhun extension protocol v1.

Usage: python3 run.py RHUN_BINARY [-k SUBSTRING] [-j N] [--quick] [--dump NAME] [--keep]

Written from the specification (extension-protocol/v1/v1.md) only.
Prints "ok NAME" or "FAIL NAME: what differed" per check; exits 1 on any failure.
"""
import argparse
import concurrent.futures
import hashlib
import glob
import json
import os
import platform
import re
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import time
import zlib

HERE = os.path.dirname(os.path.abspath(__file__))
EXT_PY = os.path.join(HERE, 'ext.py')
MANIFESTS = os.path.join(HERE, 'manifests')

# ---------------------------------------------------------------- project files

A_TXT = 'alpha\nbeta\ngamma\n'
MAIN_GO = 'package x\n\nfunc F() {}\n'
UTF_TXT = 'é' + '1\nsecond line\n'          # e-acute is two bytes: columns 1-2
DATA_JSON = '{"k": 1}\n'
CRLF_TXT = b'one\r\ntwo\r\n'
SPACED = 'spaced\n'
MAKEFILE = 'all:\n\techo hi\n'


def tiny_png():
    def chunk(t, d):
        c = struct.pack('>I', len(d)) + t + d
        return c + struct.pack('>I', zlib.crc32(t + d) & 0xffffffff)
    raw = b'\x00\xff\x00\x00'
    return (b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', 1, 1, 8, 2, 0, 0, 0))
            + chunk(b'IDAT', zlib.compress(raw)) + chunk(b'IEND', b''))


def make_project(p):
    os.makedirs(p, exist_ok=True)
    for name, data in (('a.txt', A_TXT.encode()), ('main.go', MAIN_GO.encode()),
                       ('utf.txt', UTF_TXT.encode()), ('data.json', DATA_JSON.encode()),
                       ('crlf.txt', CRLF_TXT), ('with space.txt', SPACED.encode()),
                       ('Makefile', MAKEFILE.encode()), ('pic.png', tiny_png())):
        with open(os.path.join(p, name), 'wb') as f:
            f.write(data)


EXEC_FILES = {'conf/wrapper/bin/ext', 'conf/badinterp/bin/bad'}
PLAIN_FILES = {'conf/noexec/bin/x'}


def install_exts(dest, names):
    for n in names:
        owner, name = n.split('/')
        src = os.path.join(MANIFESTS, owner, name)
        dst = os.path.join(dest, owner, name)
        for root, _, files in os.walk(src):
            for fn in files:
                sp = os.path.join(root, fn)
                rel = os.path.relpath(sp, MANIFESTS)
                dp = os.path.join(dest, rel)
                os.makedirs(os.path.dirname(dp), exist_ok=True)
                with open(sp, 'rb') as f:
                    data = f.read()
                if b'EXT_PY_LOCAL' in data:
                    # a copy of ext.py beside it: a confined extension reads only its own folder
                    local = os.path.join(os.path.dirname(dp), 'ext.py')
                    shutil.copyfile(EXT_PY, local)
                    data = data.replace(b'EXT_PY_LOCAL', local.encode())
                data = data.replace(b'EXT_PY', EXT_PY.encode())
                with open(dp, 'wb') as f:
                    f.write(data)
                os.chmod(dp, 0o755 if rel in EXEC_FILES else 0o644)


# ---------------------------------------------------------------- results

class Rec:
    def __init__(self, ts, kind, text):
        self.ts, self.kind, self.text = ts, kind, text


class Msg:
    """A line rhun sent to the extension, with its body when a reply carries one."""
    def __init__(self, ts, line):
        self.ts = ts
        self.line = line
        self.f = line.split(' ')
        self.body = None            # bytes
        self.sha = None


class Result:
    def __init__(self, scn):
        self.scn = scn
        self.checks = []            # (name, ok, message)
        self.out = []
        self.rc = None
        self.elapsed = 0.0
        self.recs = []
        self.msgs = []
        self.sent = []              # Rec of kind '>'
        self.notes = []             # Rec of kind 'X'
        self.dirs = {}
        self.segs = {}
        self.stdout = ''
        self.timed_out = False
        self.err = ''

    # ---- check helpers
    def ok(self, name, cond, msg=''):
        self.checks.append((self.scn.name + '.' + name, bool(cond), msg))
        return bool(cond)

    def eq(self, name, got, want):
        return self.ok(name, got == want, 'got %r, want %r' % (got, want))

    def has(self, name, hay, needle):
        return self.ok(name, needle in hay, 'got %r, want it to contain %r' % (hay, needle))

    # ---- log queries
    def lines(self):
        return [m.line for m in self.msgs]

    def events(self, name):
        return [m for m in self.msgs if len(m.f) > 1 and m.f[0] == '0' and m.f[1] == name]

    def mark(self, label):
        for n in self.notes:
            if n.text == 'mark ' + label:
                return n.idx
        return None

    def between(self, a, b, name=None):
        """Messages from rhun logged between two marks (labels), optionally only events `name`."""
        ia = self.mark(a) if isinstance(a, str) else a
        ib = self.mark(b) if isinstance(b, str) else b
        out = []
        for m in self.msgs:
            if (ia is None or m.idx > ia) and (ib is None or m.idx < ib):
                if name is None or (len(m.f) > 1 and m.f[0] == '0' and m.f[1] == name):
                    out.append(m)
        return out

    def finished(self):
        return self.ok('ops-finished', self.mark('done') is not None,
                       'the extension did not reach its last operation (log has %d records)' % len(self.recs))

    def notes_with(self, prefix):
        return [n for n in self.notes if n.text.startswith(prefix)]

    def reply(self, rid):
        for m in self.msgs:
            if m.f[0] in ('ok', 'error') and len(m.f) > 1 and m.f[1] == str(rid):
                return m
        return None

    def replies(self):
        return [m for m in self.msgs if m.f[0] in ('ok', 'error')]

    def sent_lines(self):
        return [s.text for s in self.sent]

    def docid(self, name):
        for m in self.events('opened'):
            g = m.line.split(' ', 5)
            if os.path.basename(g[5]) == name:
                return int(g[2])
        return None

    def opened_names(self):
        return [os.path.basename(m.line.split(' ', 5)[5]) for m in self.events('opened')]

    def env(self, name):
        for r in self.recs:
            if r.kind == 'E' and r.text.startswith(name + ' '):
                return json.loads(r.text[len(name) + 1:])
        return None

    def starts(self):
        return len(self.notes_with('start '))

    def hello_count(self):
        return len(self.notes_with('hello-sent'))

    # ---- rhun output queries
    def seg(self, label):
        return self.segs.get(label, [])

    def state(self, label):
        for ln in self.seg(label):
            if ln.startswith('tabs='):
                return dict(kv.split('=', 1) for kv in ln.split(' ') if '=' in kv)
        return {}

    def doc(self, label):
        seg = self.seg(label)
        if '<eod>' in seg:
            return '\n'.join(seg[:seg.index('<eod>')])
        return None

    EXT_RE = re.compile(r'^(\S+/\S+) state=(\S+)(?: reason=(.*?))?(?: pid=(\d+))?(?: cmds=(\S*))?(?: (?!status=)[a-z_]+=\S*)*(?: status=(.*))?$')

    def exts(self, label):
        d = {}
        for ln in self.seg(label):
            m = self.EXT_RE.match(ln)
            if m:
                d[m.group(1)] = dict(state=m.group(2), reason=m.group(3), pid=m.group(4),
                                     cmds=m.group(5), status=m.group(6), raw=ln)
        return d

    def ext(self, label, name):
        return self.exts(label).get(name)


class Scenario:
    def __init__(self, name, exts, ops=None, script=None, env=None, check=None, empty=False,
                 control=False, slow=False, timeout=60, pre=None):
        self.name = name
        self.exts = exts
        self.ops = ops
        self.script = script if script is not None else []
        self.env = env or {}
        self.check = check
        self.empty = empty
        self.control = control
        self.slow = slow
        self.timeout = timeout
        self.pre = pre


SCENARIOS = []


def scenario(name, exts, **kw):
    def deco(fn):
        SCENARIOS.append(Scenario(name, exts, check=fn, **kw))
        return fn
    return deco


def plain(name, exts, **kw):
    SCENARIOS.append(Scenario(name, exts, **kw))


# ---------------------------------------------------------------- running rhun

def parse_log(path):
    recs = []
    try:
        with open(path, 'rb') as f:
            data = f.read()
    except OSError:
        return recs
    for raw in data.split(b'\n'):
        if not raw:
            continue
        s = raw.decode('utf-8', 'replace')
        parts = s.split(' ', 2)
        if len(parts) < 2:
            continue
        try:
            ts = float(parts[0])
        except ValueError:
            continue
        recs.append(Rec(ts, parts[1], parts[2] if len(parts) > 2 else ''))
    return recs


def build_msgs(res):
    cur = None
    for i, r in enumerate(res.recs):
        r.idx = i
        if r.kind == '<':
            cur = Msg(r.ts, r.text)
            cur.idx = i
            res.msgs.append(cur)
        elif r.kind == 'B' and cur is not None:
            n, sha, js = r.text.split(' ', 2)
            cur.sha = sha
            if js != '-':
                cur.body = json.loads(js).encode('utf-8', 'surrogateescape')
            cur.blen = int(n)
        elif r.kind == '>':
            res.sent.append(r)
        elif r.kind == 'X':
            res.notes.append(r)


def split_segments(lines):
    segs = {}
    label = None
    for ln in lines:
        if ln.startswith('@') and re.match(r'^@\S+$', ln):
            label = ln[1:]
            segs[label] = []
        elif label is not None:
            segs[label].append(ln)
    return segs


def run_scenario(scn, rhun, keep=False):
    res = Result(scn)
    base = os.path.realpath(tempfile.mkdtemp(prefix='rhc-'))
    d = dict(base=base, proj=os.path.join(base, 'proj'), home=os.path.join(base, 'home'),
             cfg=os.path.join(base, 'cfg'), state=os.path.join(base, 'state'),
             ext=os.path.join(base, 'ext'), log=os.path.join(base, 'ext.log'),
             script=os.path.join(base, 'script.txt'))
    res.dirs = d
    for k in ('home', 'cfg', 'state', 'ext'):
        os.makedirs(d[k])
    make_project(d['proj'])
    install_exts(d['ext'], scn.exts)
    if scn.pre:
        scn.pre(d)
    script = [ln.format(P=d['proj'], S=base) for ln in scn.script]
    with open(d['script'], 'w') as f:
        f.write('\n'.join(script) + '\n')
    env = {'PATH': os.environ.get('PATH', '/usr/bin:/bin'), 'HOME': d['home'],
           'XDG_CONFIG_HOME': d['cfg'], 'XDG_STATE_HOME': d['state'],
           'LANG': 'en_US.UTF-8', 'LC_ALL': 'en_US.UTF-8', 'TZ': 'UTC',
           'CONF_LOG': d['log'], 'CONF_MARK': 'inherited'}
    if isinstance(scn.ops, dict):           # {extension name: ops} when several extensions run at once
        for k, v in scn.ops.items():
            env['CONF_OPS_' + k.upper()] = json.dumps(v)
    elif scn.ops is not None:
        env['CONF_OPS'] = json.dumps(scn.ops)
    for k, v in scn.env.items():
        env[k] = v.format(P=d['proj'], S=base) if isinstance(v, str) else v
    argv = [rhun]
    if not scn.empty:
        argv.append(d['proj'])
    else:
        argv.append('--empty')
    argv += ['--headless', '1000x600', '--scale', '1', '--extensions', d['ext']]
    if scn.control:
        sock = tempfile.mktemp(prefix='rc', dir='/tmp')
        d['sock'] = sock
        argv += ['--control', sock]
    argv += ['--script', d['script']]
    t0 = time.time()
    p = subprocess.Popen(argv, cwd=d['base'], env=env, stdin=subprocess.DEVNULL,
                         stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    try:
        out, err = p.communicate(timeout=scn.timeout)
    except subprocess.TimeoutExpired:
        res.timed_out = True
        try:
            os.killpg(p.pid, signal.SIGKILL)
        except OSError:
            pass
        out, err = p.communicate()
    res.elapsed = time.time() - t0
    res.rc = p.returncode
    res.stdout = out.decode('utf-8', 'replace')
    res.err = err.decode('utf-8', 'replace')
    res.out = res.stdout.split('\n')
    res.segs = split_segments(res.out)
    res.recs = parse_log(d['log'])
    for log in sorted(glob.glob(os.path.join(d['state'], 'rhun', 'extensions', '*', 'conf.log'))):
        res.recs += parse_log(log)
    res.recs.sort(key=lambda r: r.ts)
    build_msgs(res)
    # leak check: every extension process we saw must be gone now
    time.sleep(0.1)
    leaked = []
    for n in res.notes_with('pid '):
        pid = int(n.text.split()[1])
        if alive(pid):
            leaked.append(pid)
    for n in res.notes_with('child '):
        pid = int(n.text.split()[1])
        if alive(pid):
            leaked.append(pid)
    res.leaked = leaked
    for pid in leaked:
        try:
            os.kill(pid, signal.SIGKILL)
        except OSError:
            pass
    if scn.check:
        try:
            scn.check(res)
        except Exception as e:      # a bug in a check is reported, not hidden
            import traceback
            res.ok('checker-bug', False, traceback.format_exc().strip().splitlines()[-1])
    why = []
    if res.timed_out:
        why.append('rhun did not exit within %ds' % scn.timeout)
    elif res.rc != 0:
        why.append('rhun exit status %s' % res.rc)
    if leaked:
        why.append('processes still alive after rhun exited: %s' % leaked)
    res.ok('clean-exit', not why, '; '.join(why))
    if keep:
        res.dirs['kept'] = True
    else:
        shutil.rmtree(base, ignore_errors=True)
        if d.get('sock'):
            try:
                os.unlink(d['sock'])
            except OSError:
                pass
    return res


def alive(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    # a zombie still answers kill(0): treat it as gone
    try:
        st = subprocess.run(['ps', '-o', 'stat=', '-p', str(pid)], capture_output=True, text=True).stdout.strip()
        return bool(st) and not st.startswith('Z')
    except Exception:
        return True


def sha(b):
    return hashlib.sha256(b).hexdigest()


# ================================================================ scenarios


# ---------------------------------------------------------------- small helpers

def pe(label):
    return ['echo @' + label, 'print-extensions']


def ps(label):
    return ['echo @' + label, 'print-state']


def pd(label):
    return ['echo @' + label, 'print-doc']


def std(pre=(), post=(), wait=1500, cmd=('cmd full.go', 'wait-ext full running'), extra=()):
    return list(pre) + list(cmd) + ['wait %d' % wait] + list(extra) + list(post) + ['quit']


DONE = ["mark", "done"]
OPEN_A = 'open {P}/a.txt'
OPEN_GO = 'open {P}/main.go'


def check_cases(res, cases, first_id=1):
    """cases: list of (label, expect); request ids are first_id, first_id+1, ... in order.

    expect:  ('body', bytes)   ok ID VERSION LEN + body
             ('err', code)     error ID code [text]
             ('ok',)           exactly `ok ID`
             ('okv',)          `ok ID N`
             ('stale',)        error ID stale N
    Returns {label: Msg or None}."""
    out = {}
    for i, (label, exp) in enumerate(cases):
        rid = first_id + i
        m = res.reply(rid)
        out[label] = m
        if m is None:
            e0 = [x.line for x in res.msgs if x.f[0] == 'error' and len(x.f) > 1 and x.f[1] == '0']
            res.ok(label, False, 'no reply to request %d%s' % (rid, (' (the host sent %r instead)' % e0[0]) if e0 else ''))
            continue
        kind = exp[0]
        if kind == 'body':
            mt = re.match(r'^ok %d (\d+) (\d+)$' % rid, m.line)
            if not mt:
                res.ok(label, False, 'got %r, want "ok %d VERSION LEN" + body' % (m.line, rid))
                continue
            want = exp[1]
            ok = int(mt.group(2)) == len(want) and m.sha == sha(want)
            if m.body is not None:
                ok = ok and m.body == want
            res.ok(label, ok, 'got %r with body %r (sha %s), want LEN %d and body %r' %
                   (m.line, m.body, (m.sha or '')[:12], len(want), want if len(want) < 200 else want[:60] + b'...'))
        elif kind == 'err':
            code = exp[1]
            res.ok(label, re.match(r'^error %d %s( .*)?$' % (rid, re.escape(code)), m.line) is not None,
                   'got %r, want "error %d %s ..."' % (m.line, rid, code))
        elif kind == 'errx':        # exact line
            res.eq(label, m.line, exp[1] % {'id': rid})
        elif kind == 'ok':
            res.eq(label, m.line, 'ok %d' % rid)
        elif kind in ('okv', 'okdoc'):
            res.ok(label, re.match(r'^ok %d \d+$' % rid, m.line) is not None, 'got %r, want "ok %d N"' % (m.line, rid))
        elif kind == 'stale':
            res.ok(label, re.match(r'^error %d stale \d+$' % rid, m.line) is not None,
                   'got %r, want "error %d stale CURRENTVERSION"' % (m.line, rid))
    return out


def ver_of(m):
    """VERSION of an `ok ID VERSION LEN` reply, or the NEWVERSION of an `ok ID NEWVERSION`."""
    return int(m.f[2])


def lang_of(res, name):
    for m in res.events('opened'):
        g = m.line.split(' ', 5)
        if os.path.basename(g[5]) == name:
            return g[4]
    return None


RHUN_VERSION_CACHE = {}


def rhun_version(rhun):
    if rhun not in RHUN_VERSION_CACHE:
        p = subprocess.run([rhun, '--version'], capture_output=True, text=True)
        RHUN_VERSION_CACHE[rhun] = (p.stdout + p.stderr).strip()
    return RHUN_VERSION_CACHE[rhun]


RHUN = {'path': None}


def host_target():
    s = platform.system()
    m = platform.machine().lower()
    if s == 'Darwin':
        return 'macos-arm64' if m in ('arm64', 'aarch64') else 'macos-x86_64'
    if s == 'Linux':
        return 'linux-x86_64'
    return 'windows-x64'


def under(path, root):
    return os.path.realpath(path).startswith(os.path.realpath(root) + os.sep)


# ================================================================ section 2: manifests and activation

ALL_BAD = ['conf/noexec', 'conf/missing', 'conf/noruntime', 'conf/norun', 'conf/miss-version',
           'conf/miss-protocol', 'conf/miss-title', 'conf/badname', 'conf/badowner', 'conf/badparse']


def chk_manifest_listing(r):
    ex = r.exts('l')
    for n in ('conf/full', 'conf/rd', 'conf/nocap', 'conf/badinterp'):
        e = ex.get(n)
        if e is None:
            r.ok('listed-%s' % n.split('/')[1], False, 'no line for %s in print-extensions' % n)
            continue
        r.ok('listed-%s' % n.split('/')[1], e['state'] != 'running' and e['reason'] is None, 'got %r' % e['raw'])
    f = ex.get('conf/full')
    if f:
        r.eq('commands-before-start', sorted((f['cmds'] or '').split(',')), ['full.go', 'full.go2'])
        r.ok('not-started-until-needed', f['state'] != 'running' and (f['pid'] in (None, '0')), 'got %r' % f['raw'])
    for n in ALL_BAD:
        short = n.split('/')[1]
        e = ex.get(n)
        if e is None:
            hits = [ln for ln in r.seg('l') if short in ln]
            e = None
            if hits:
                m = Result.EXT_RE.match(hits[0])
                if m:
                    e = dict(state=m.group(2), reason=m.group(3), pid=m.group(4), raw=hits[0])
        if e is None:
            r.ok('bad-%s' % short, False, 'no line mentioning %s in print-extensions (a manifest that cannot be used is "recorded with its reason")' % short)
            continue
        r.ok('bad-%s' % short, e['state'] != 'running' and bool(e['reason']) and e['pid'] in (None, '0'),
             'want a non-running entry with a reason and no pid, got %r' % e['raw'])
    r.ok('bad-never-started', r.starts() == 0, 'extension processes were started: %d' % r.starts())


plain('manifest.listing', ['conf/full', 'conf/rd', 'conf/nocap', 'conf/badinterp'] + ALL_BAD,
      script=['wait 300'] + pe('l') + ['cmd noexec.go', 'cmd missing.go', 'cmd norun.go', 'wait 500', 'quit'],
      check=chk_manifest_listing)


def chk_act_startup(r):
    e = r.ext('s', 'conf/startup')
    r.ok('running-without-command', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))
    r.eq('one-hello', r.hello_count(), 1)
    ready = r.events('ready')
    r.ok('ready-delivered', len(ready) == 1, 'ready events: %d' % len(ready))


plain('act.startup', ['conf/startup'], ops=[DONE], script=['wait 1200'] + pe('s') + ['quit'], check=chk_act_startup)


def chk_act_command_only(r):
    e = r.ext('a', 'conf/full')
    r.ok('idle-with-documents-open', e is not None and e['state'] != 'running' and r.starts() == 0,
         'got %r, starts %d' % (e and e['raw'], r.starts()))


plain('act.command-default', ['conf/full'], ops=[DONE],
      script=[OPEN_A, OPEN_GO, 'wait 700'] + pe('a') + ['quit'], check=chk_act_command_only)


def chk_act_lang(r):
    b, c = r.ext('b', 'conf/onlang'), r.ext('c', 'conf/onlang')
    r.ok('not-started-for-other-language', b is not None and b['state'] != 'running', 'got %r' % (b and b['raw']))
    r.ok('started-when-Go-document-opens', c is not None and c['state'] == 'running', 'got %r' % (c and c['raw']))
    r.ok('catch-up-has-the-Go-document', 'main.go' in r.opened_names(), 'opened: %r' % r.opened_names())


plain('act.lang', ['conf/onlang'], ops=[DONE],
      script=[OPEN_A, 'wait 600'] + pe('b') + [OPEN_GO, 'wait 1200'] + pe('c') + ['quit'], check=chk_act_lang)


def chk_act_open(r):
    b, c = r.ext('b', 'conf/onopen'), r.ext('c', 'conf/onopen')
    r.ok('not-started-for-other-file', b is not None and b['state'] != 'running', 'got %r' % (b and b['raw']))
    r.ok('started-when-matching-file-opens', c is not None and c['state'] == 'running', 'got %r' % (c and c['raw']))
    r.ok('catch-up-has-the-json-document', 'data.json' in r.opened_names(), 'opened: %r' % r.opened_names())


plain('act.open-glob', ['conf/onopen'], ops=[DONE],
      script=[OPEN_A, 'wait 600'] + pe('b') + ['open {P}/data.json', 'wait 1200'] + pe('c') + ['quit'], check=chk_act_open)


def chk_act_project(r):
    e = r.ext('p', 'conf/onproj')
    r.ok('started-with-a-project', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))
    ready = r.events('ready')
    r.ok('ready-has-the-project', len(ready) == 1 and ready[0].f[2] == r.dirs['proj'], 'got %r' % [m.line for m in ready])


plain('act.project', ['conf/onproj'], ops=[DONE], script=['wait 1200'] + pe('p') + ['quit'], check=chk_act_project)


def chk_act_project_none(r):
    e = r.ext('p', 'conf/onproj')
    r.ok('not-started-without-a-project', e is not None and e['state'] != 'running' and r.starts() == 0,
         'got %r, starts %d' % (e and e['raw'], r.starts()))


plain('act.project-none', ['conf/onproj'], ops=[DONE], empty=True, script=['wait 1000'] + pe('p') + ['quit'],
      check=chk_act_project_none)


def chk_wrapper(r):
    e = r.ext('w', 'conf/wrapper')
    r.ok('started-from-run-target', e is not None and e['state'] == 'running' and r.starts() == 1,
         'got %r, starts %d' % (e and e['raw'], r.starts()))
    cwd = r.notes_with('cwd ')
    r.ok('cwd-is-the-extension-folder', bool(cwd) and os.path.realpath(json.loads(cwd[0].text[4:])) ==
         os.path.realpath(os.path.join(r.dirs['ext'], 'conf', 'wrapper')), 'got %r' % [c.text for c in cwd])
    r.eq('hello-accepted', len(r.events('ready')), 1)


plain('start.run-target', ['conf/wrapper'], ops=[DONE],
      script=std(cmd=('cmd wrapper.go', 'wait-ext wrapper running'), wait=1000, post=pe('w')), check=chk_wrapper)


def chk_args(r):
    argv = r.notes_with('argv ')
    a = json.loads(argv[0].text[5:]) if argv else None
    r.eq('runtime-run-and-args', a, [EXT_PY, 'one', 'two'])
    cwd = r.notes_with('cwd ')
    r.ok('cwd-is-the-extension-folder', bool(cwd) and os.path.realpath(json.loads(cwd[0].text[4:])) ==
         os.path.realpath(os.path.join(r.dirs['ext'], 'conf', 'args')), 'got %r' % [c.text for c in cwd])


plain('start.runtime-args', ['conf/args'], ops=[DONE],
      script=std(cmd=('cmd args.go', 'wait-ext args running'), wait=800), check=chk_args)


def chk_bad_interp(r):
    e = r.ext('f', 'conf/badinterp')
    r.ok('not-running', e is not None and e['state'] != 'running', 'got %r' % (e and e['raw']))
    r.ok('reason-wording', e is not None and e['reason'] is not None and
         'failed to start: the program could not be executed' in e['reason'], 'got %r' % (e and e['raw']))
    r.ok('commands-stay', e is not None and e['cmds'] == 'badinterp.go', 'got %r' % (e and e['raw']))


plain('start.bad-interpreter', ['conf/badinterp'],
      script=['cmd badinterp.go', 'wait 800'] + pe('f') + ['quit'], check=chk_bad_interp)

# ================================================================ section 4.1: the environment

def chk_env(r):
    st = os.path.join(r.dirs['state'], 'rhun', 'extensions', 'conf--full')
    E = r.env

    def same(name, got, want):
        r.ok(name, got is not None and os.path.realpath(got) == os.path.realpath(want), 'got %r, want %r' % (got, want))

    r.eq('RHUN_PROTOCOL', E('RHUN_PROTOCOL'), '1.1')
    r.eq('RHUN_EXTENSION', E('RHUN_EXTENSION'), 'conf/full')
    r.eq('RHUN_TARGET', E('RHUN_TARGET'), host_target())
    ver = rhun_version(RHUN['path'])
    r.ok('RHUN_VERSION', bool(E('RHUN_VERSION')) and E('RHUN_VERSION') in ver,
         'got %r, `rhun --version` printed %r' % (E('RHUN_VERSION'), ver))
    same('RHUN_STATE', E('RHUN_STATE'), st)
    same('RHUN_PROJECT', E('RHUN_PROJECT'), r.dirs['proj'])
    r.eq('RHUN_SOCKET-unset-without-control', E('RHUN_SOCKET'), None)
    same('HOME-is-the-state-folder', E('HOME'), st)
    same('TMPDIR-is-STATE/tmp', E('TMPDIR'), os.path.join(st, 'tmp'))
    r.eq('TMPDIR-exists', [n.text for n in r.notes_with('tmpdir-exists')], ['tmpdir-exists True'])
    for v in ('XDG_CACHE_HOME', 'GOCACHE', 'GOMODCACHE', 'GOPATH', 'npm_config_cache'):
        got = E(v)
        r.ok(v + '-under-state', got is not None and under(got, st), 'got %r, want a path under %s' % (got, st))
    r.eq('LANG', E('LANG'), 'en_US.UTF-8')
    r.eq('LC_ALL', E('LC_ALL'), 'en_US.UTF-8')
    r.eq('TZ', E('TZ'), 'UTC')
    r.eq('PATH', E('PATH'), os.environ.get('PATH', '/usr/bin:/bin'))
    r.eq('inherits-rhun-environment', E('CONF_MARK'), 'inherited')
    r.ok('HOME-is-not-rhuns-HOME', E('HOME') != r.dirs['home'], 'HOME is still %r' % E('HOME'))
    cwd = r.notes_with('cwd ')
    r.ok('cwd-is-the-extension-folder', bool(cwd) and os.path.realpath(json.loads(cwd[0].text[4:])) ==
         os.path.realpath(os.path.join(r.dirs['ext'], 'conf', 'full')), 'got %r' % [c.text for c in cwd])
    r.ok('state-folder-created', os.path.isdir(st), '%s is not a directory' % st)
    log = st + '.log'
    try:
        with open(log, 'rb') as f:
            data = f.read()
    except OSError as e:
        data = None
    r.ok('stderr-goes-to-OWNER--NAME.log', data is not None and b'ext-stderr-marker' in data,
         'could not find the extension\'s stderr line in %s (%r)' % (log, data and data[:80]))


plain('env.table', ['conf/full'], ops=[DONE], script=std(pre=[OPEN_A], wait=700), check=chk_env)


def chk_env_socket(r):
    got = r.env('RHUN_SOCKET')
    r.ok('RHUN_SOCKET-is-the-control-path', got == r.dirs.get('sock'), 'got %r, want %r' % (got, r.dirs.get('sock')))


plain('env.socket', ['conf/full'], ops=[DONE], control=True, script=std(wait=700), check=chk_env_socket)


def chk_env_noproject(r):
    r.eq('RHUN_PROJECT-unset', r.env('RHUN_PROJECT'), None)
    ready = r.events('ready')
    r.eq('ready--', [m.line for m in ready], ['0 ready -'])
    r.ok('RHUN_STATE-still-set', bool(r.env('RHUN_STATE')), 'RHUN_STATE is %r' % r.env('RHUN_STATE'))


plain('env.no-project', ['conf/full'], ops=[DONE], empty=True, script=std(wait=700), check=chk_env_noproject)


def pre_big_log(d):
    p = os.path.join(d['state'], 'rhun', 'extensions')
    os.makedirs(p, exist_ok=True)
    with open(os.path.join(p, 'conf--full.log'), 'wb') as f:
        f.write(b'old-log-line\n' * 100000)           # 1.3 MB


def chk_log_cut_start(r):
    log = os.path.join(r.dirs['state'], 'rhun', 'extensions', 'conf--full.log')
    data = open(log, 'rb').read()
    r.ok('log-over-1MB-is-cut-back', b'old-log-line' not in data and b'ext-stderr-marker' in data,
         'size %d, still has old content: %s' % (len(data), b'old-log-line' in data))


plain('env.log-cut-at-start', ['conf/full'], ops=[DONE], pre=pre_big_log, script=std(wait=700), check=chk_log_cut_start)


def chk_log_cut_live(r):
    log = os.path.join(r.dirs['state'], 'rhun', 'extensions', 'conf--full.log')
    size = os.path.getsize(log)
    r.finished()
    r.ok('log-written-while-running-is-cut-back-past-1MB', size < 1100000,
         'size %d after the program wrote 1.5 MB (it should have been cut back to empty when it passed 1 MB)' % size)


plain('env.log-cut-live', ['conf/full'], ops=[["stderr", 1500000], ["sleep", 1.5], ["stderrmsg", "tail-marker"], DONE],
      script=std(wait=4000), check=chk_log_cut_live)

# ================================================================ section 4.2-4.3: handshake and catch-up


def chk_catchup(r):
    msgs = r.lines()
    proj = r.dirs['proj']
    r.eq('first-line-is-ready', msgs[0] if msgs else None, '0 ready ' + proj)
    ids = {}
    for m in r.events('opened'):
        mt = re.match(r'^0 opened (\d+) (\d+) (\S+) (.+)$', m.line)
        if not mt:
            r.ok('opened-shape', False, 'got %r, want "0 opened DOC VERSION LANG PATH"' % m.line)
            continue
        ids[os.path.basename(mt.group(4))] = (int(mt.group(1)), mt.group(3), mt.group(4))
    r.eq('opened-set', sorted(ids), ['a.txt', 'main.go', 'utf.txt', 'with space.txt'])
    r.eq('opened-LANG-for-Go', ids.get('main.go', (0, None))[1], 'Go')
    r.eq('opened-LANG-for-no-grammar', ids.get('a.txt', (0, None))[1], '-')
    r.eq('opened-PATH-absolute', sorted(v[2] for v in ids.values()),
         sorted(os.path.join(proj, n) for n in ids))
    r.eq('opened-PATH-with-space-is-whole', ids.get('with space.txt', (0, 0, None))[2], os.path.join(proj, 'with space.txt'))
    r.ok('opened-ids-distinct', len(set(v[0] for v in ids.values())) == len(ids), 'ids: %r' % ids)
    idx = lambda name: [m.idx for m in r.events(name)]
    act = r.events('active')
    r.eq('active-is-the-last-opened-tab', [m.line for m in act], ['0 active %d' % ids['with space.txt'][0]] if 'with space.txt' in ids else None)
    cur = r.events('cursor')
    r.eq('cursor-for-the-active-document', [m.line for m in cur], ['0 cursor %d 1 1' % ids['with space.txt'][0]] if 'with space.txt' in ids else None)
    order_ok = (idx('ready') and idx('opened') and idx('active') and cur and
                max(idx('ready')) < min(idx('opened')) and max(idx('opened')) < min(idx('active')) and
                max(idx('active')) < min(m.idx for m in cur))
    r.ok('order-ready-opened-active-cursor', order_ok,
         'order of events: %s' % [m.f[1] for m in r.msgs if m.f[0] == '0'][:10])
    r.eq('one-ready', len(r.events('ready')), 1)
    vers = [int(m.f[3]) for m in r.events('opened') if len(m.f) > 3 and m.f[3].isdigit()]
    r.ok('opened-VERSION-integer', len(vers) == 4, 'versions: %r' % vers)


plain('hs.catch-up', ['conf/full'], ops=[DONE],
      script=['open {P}/a.txt', 'open {P}/utf.txt', OPEN_GO, 'open {P}/with space.txt', 'cmd full.go',
              'wait-ext full running', 'wait 900', 'quit'], check=chk_catchup)


def chk_catchup_selection(r):
    st = r.state('s')
    r.ok('precondition-selection-exists', st.get('sel') not in (None, '0'), 'print-state: %r' % st)
    doc = r.docid('a.txt')
    lines = r.lines()
    r.ok('catch-up-cursor', '0 cursor %d 1 3' % doc in lines, 'want "0 cursor %d 1 3" in the catch-up, saw cursor events %r (only a selection event was sent)' % (doc, [l for l in lines if l.startswith('0 c')]))
    r.ok('catch-up-selection-anchor-then-cursor', '0 selection %d 1 1 1 3' % doc in lines,
         'got events %r' % [l for l in lines if l.startswith('0 s')])


plain('hs.catch-up-selection', ['conf/full'], ops=[DONE],
      script=[OPEN_A, 'key shift+Right', 'key shift+Right'] + ps('s') + ['cmd full.go', 'wait-ext full running', 'wait 800', 'quit'],
      check=chk_catchup_selection)


def chk_catchup_none(r):
    st = r.state('s')
    r.eq('precondition-no-tabs', st.get('tabs'), '0')
    r.eq('no-opened', len(r.events('opened')), 0)
    r.eq('active-0', [m.line for m in r.events('active')], ['0 active 0'])
    runs = [m.line for m in r.events('run')]
    r.eq('run-has-DOC-0', runs, ['0 run full.go 0'])
    r.eq('ready', [m.line for m in r.events('ready')], ['0 ready ' + r.dirs['proj']])


plain('hs.no-documents', ['conf/full'], ops=[DONE],
      script=[OPEN_A, 'cmd close_tab'] + ps('s') + ['cmd full.go', 'wait-ext full running', 'wait 800', 'quit'],
      check=chk_catchup_none)


def chk_untitled(r):
    ops = [m.line for m in r.events('opened')]
    r.ok('untitled-PATH-is-dash', len(ops) == 1 and re.match(r'^0 opened \d+ \d+ - -$', ops[0]) is not None,
         'got %r, want "0 opened DOC VERSION - -"' % ops)


plain('hs.untitled-document', ['conf/full'], ops=[DONE],
      script=['cmd new_file', 'cmd full.go', 'wait-ext full running', 'wait 800', 'quit'], check=chk_untitled)


def chk_start_expect_running(r):
    e = r.ext('e', 'conf/full')
    r.ok('running', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))
    r.eq('ready-delivered', len(r.events('ready')), 1)


plain('hs.hello-late', ['conf/full'], ops=[DONE], env={'CONF_HELLO_DELAY': '3.5'}, slow=True,
      script=['cmd full.go', 'wait 5200'] + pe('e') + ['quit'], check=chk_start_expect_running)

plain('hs.hello-with-capabilities', ['conf/full'], ops=[DONE], env={'CONF_HELLO': 'hello 1 future-cap other\\n'},
      script=['cmd full.go', 'wait 1200'] + pe('e') + ['quit'], check=chk_start_expect_running)


def chk_crlf(r):
    e = r.ext('e', 'conf/full')
    r.ok('hello-with-CRLF-accepted', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))
    check_cases(r, CRLF_CASES)
    r.finished()


CRLF_STEPS = [
    ('text', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('toast', ["toast", "hi"], ('ok',)),
    ('status', ["status", "s"], ('ok',)),
    ('replace-with-CRLF-header', ["replace", "a.txt", "cur", 1, 1, 1, 6, "ALPHA"], ('okv',)),
    ('text-after', ["text", "a.txt"], ('body', b'ALPHA\nbeta\ngamma\n')),
    DONE,
]
CRLF_OPS, CRLF_CASES = None, None


def split_steps(steps):
    ops, cases = [], []
    for it in steps:
        if len(it) == 3 and isinstance(it[0], str) and isinstance(it[1], list):
            ops.append(it[1])
            cases.append((it[0], it[2]))
        else:
            ops.append(it)
    return ops, cases


CRLF_OPS, CRLF_CASES = split_steps(CRLF_STEPS)
plain('hs.CRLF-line-ends', ['conf/full'], ops=CRLF_OPS, env={'CONF_CRLF': '1'},
      script=std(pre=[OPEN_A], wait=1500, post=pe('e')), check=chk_crlf)


def chk_hello_coalesced(r):
    r.eq('ready-after-hello', len(r.events('ready')), 1)
    r.eq('request-in-the-hello-write-is-answered', [m.line for m in r.replies()], ['ok 1'])


plain('hs.hello-and-request-in-one-write', ['conf/full'], ops=[DONE],
      env={'CONF_HELLO': 'hello 1\\n1 toast 2\\nhi'}, script=std(wait=900), check=chk_hello_coalesced)


def ended_after_start(r, within=None):
    """Seconds from the extension's start until its last sign of life (heartbeat or exit record)."""
    st = r.notes_with('start ')
    if not st:
        return None
    last = [n for n in r.notes if n.text == 'alive' or n.text.startswith('sigterm') or n.text.startswith('exit ')]
    if not last:
        return None
    return max(n.ts for n in last) - st[0].ts


HB = {'CONF_HEARTBEAT': '1'}


def chk_nohello(r):
    e = r.ext('e', 'conf/full')
    r.ok('not-running', e is not None and e['state'] != 'running', 'got %r' % (e and e['raw']))
    r.ok('reason-wording', e is not None and e['reason'] is not None and 'no hello within 5 s' in e['reason'],
         'got %r, want a reason containing "no hello within 5 s"' % (e and e['raw']))
    t = ended_after_start(r, 7)
    r.ok('ended-after-about-5-seconds', t is not None and 4.5 <= t <= 7.0,
         'the process ended %s s after it started (want about 5)' % (None if t is None else round(t, 2)))
    r.eq('no-ready-sent', len(r.events('ready')), 0)


plain('hs.no-hello', ['conf/full'], env=dict(HB, CONF_NOHELLO='1'), slow=True,
      script=['cmd full.go', 'wait 6400'] + pe('e') + ['quit'], check=chk_nohello)


def chk_wrong_first(r):
    e = r.ext('e', 'conf/full')
    r.ok('not-running', e is not None and e['state'] != 'running', 'got %r' % (e and e['raw']))
    r.ok('reason-wording', e is not None and e['reason'] is not None and 'the first line was not hello 1' in e['reason'],
         'got %r, want a reason containing "the first line was not hello 1"' % (e and e['raw']))
    t = ended_after_start(r, 3)
    r.ok('ended-at-once', t is not None and t < 3.0, 'ended %s s after start' % (None if t is None else round(t, 2)))
    r.eq('request-before-hello-not-executed', [m.line for m in r.replies()], [])
    r.eq('no-ready-sent', len(r.events('ready')), 0)


plain('hs.first-line-not-hello', ['conf/full'], env=dict(HB, CONF_HELLO='1 toast 2\\nhi'),
      script=['cmd full.go', 'wait 1500'] + pe('e') + ['quit'], check=chk_wrong_first)


def chk_hello2(r):
    e = r.ext('e', 'conf/full')
    r.ok('not-running', e is not None and e['state'] != 'running', 'got %r' % (e and e['raw']))
    r.ok('reason-wording', e is not None and e['reason'] is not None and
         'protocol version not supported (rhun speaks 1)' in e['reason'],
         'got %r, want a reason containing "protocol version not supported (rhun speaks 1)"' % (e and e['raw']))
    t = ended_after_start(r, 3)
    r.ok('ended-at-once', t is not None and t < 3.0, 'ended %s s after start' % (None if t is None else round(t, 2)))
    r.eq('no-ready-sent', len(r.events('ready')), 0)


plain('hs.hello-version-2', ['conf/full'], env=dict(HB, CONF_HELLO='hello 2\\n'),
      script=['cmd full.go', 'wait 1500'] + pe('e') + ['quit'], check=chk_hello2)

# ================================================================ section 2: commands and the run event


def chk_run_events2(r):
    docs = {n: r.docid(n) for n in ('a.txt', 'main.go')}
    runs = [m.line for m in r.events('run')]
    r.eq('run-events-with-the-active-document', runs,
         ['0 run full.go %d' % docs['main.go'], '0 run full.go2 %d' % docs['main.go'],
          '0 run full.go %d' % docs['a.txt']])
    ready_i = min(m.idx for m in r.events('ready'))
    r.ok('run-after-ready', all(m.idx > ready_i for m in r.events('run')), 'run before ready')
    r.eq('one-process', r.starts(), 1)


plain('cmd.run-event', ['conf/full'], ops=[DONE],
      script=[OPEN_A, OPEN_GO, 'cmd full.go', 'wait-ext full running', 'wait 700', 'cmd full.go2', 'wait 300',
              'open {P}/a.txt', 'cmd full.go', 'wait 400', 'quit'], check=chk_run_events2)


def chk_cmd_replace(r):
    runs = [m.line for m in r.events('run')]
    gid = r.docid('main.go')
    r.eq('only-the-second-command-is-delivered', runs, ['0 run full.go2 %d' % gid] if gid else None)
    r.eq('one-process-for-both', r.starts(), 1)
    if r.events('ready') and r.events('run'):
        r.ok('delivered-after-ready', r.events('run')[0].idx > r.events('ready')[0].idx, 'run came before ready')


plain('cmd.while-starting-second-replaces-first', ['conf/full'], ops=[DONE], env={'CONF_HELLO_DELAY': '1.0'},
      script=[OPEN_A, OPEN_GO, 'cmd full.go', 'cmd full.go2', 'wait 2800', 'quit'], check=chk_cmd_replace)

# ================================================================ sections 6 and 8: text and range

UTF = UTF_TXT.encode()
READ_STEPS = [
    ('text', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('text-CRLF-document-has-no-CR', ["text", "crlf.txt"], ('body', b'one\ntwo\n')),
    ('text-multibyte', ["text", "utf.txt"], ('body', UTF)),
    ('text-no-such-document', ["text", 424242], ('err', 'nodoc')),
    ('range-one-line-with-its-LF', ["range", "a.txt", 2, 1, 3, 1], ('body', b'beta\n')),
    ('range-inside-a-line', ["range", "a.txt", 1, 2, 1, 4], ('body', b'lp')),
    ('range-whole-document', ["range", "a.txt", 1, 1, 4, 1], ('body', A_TXT.encode())),
    ('range-empty', ["range", "a.txt", 1, 1, 1, 1], ('body', b'')),
    ('range-up-to-the-LF-position', ["range", "a.txt", 3, 1, 3, 6], ('body', b'gamma')),
    ('range-the-LF-alone', ["range", "a.txt", 3, 6, 4, 1], ('body', b'\n')),
    ('range-at-end-of-document', ["range", "a.txt", 4, 1, 4, 1], ('body', b'')),
    ('range-two-byte-character', ["range", "utf.txt", 1, 1, 1, 3], ('body', 'é'.encode())),
    ('range-after-two-byte-character', ["range", "utf.txt", 1, 3, 1, 4], ('body', b'1')),
    ('range-CRLF-document-on-stored-bytes', ["range", "crlf.txt", 1, 1, 2, 1], ('body', b'one\n')),
    ('range-Go-document', ["range", "main.go", 1, 1, 1, 8], ('body', b'package')),
    ('badpos-start-inside-character', ["range", "utf.txt", 1, 2, 1, 3], ('err', 'badpos')),
    ('badpos-end-inside-character', ["range", "utf.txt", 1, 1, 1, 2], ('err', 'badpos')),
    ('badpos-line-beyond-document', ["range", "a.txt", 1, 1, 9, 1], ('err', 'badpos')),
    ('badpos-column-beyond-the-LF', ["range", "a.txt", 1, 7, 1, 8], ('err', 'badpos')),
    ('badpos-past-end-of-document', ["range", "a.txt", 4, 2, 4, 2], ('err', 'badpos')),
    ('badpos-line-0', ["range", "a.txt", 0, 1, 1, 1], ('err', 'badpos')),
    ('range-no-such-document', ["range", 424242, 1, 1, 1, 1], ('err', 'nodoc')),
    ('highest-request-id', ["text", "a.txt", {"id": 2147483647}], ('body', A_TXT.encode())),
    DONE,
]
READ_OPS, READ_CASES = split_steps(READ_STEPS)


def case_ids(steps):
    """Request ids the extension uses for the cases of `steps`, in order."""
    ids, nxt = [], 1
    for it in steps:
        if len(it) == 3 and isinstance(it[0], str) and isinstance(it[1], list):
            opts = it[1][-1] if isinstance(it[1][-1], dict) and 'id' in it[1][-1] else {}
            rid = opts.get('id', nxt)
            ids.append(rid)
            nxt = max(nxt, rid) + 1
    return ids


def check_steps(res, steps):
    """check_cases with the request ids the steps really use."""
    ops, cases = split_steps(steps)
    ids = case_ids(steps)
    out = {}
    for rid, (label, exp) in zip(ids, cases):
        out.update(check_cases(res, [(label, exp)], first_id=rid))
    return out


def chk_read(r):
    r.finished()
    got = check_steps(r, READ_STEPS)
    opened = {m.f[2]: int(m.f[3]) for m in r.events('opened')}
    a = r.docid('a.txt')
    t = got.get('text')
    r.ok('text-VERSION-equals-opened-VERSION', t is not None and t.f[0] == 'ok' and int(t.f[2]) == opened.get(str(a)),
         'text reply %r, opened VERSION %r' % (t and t.line, opened.get(str(a))))
    g = got.get('range-whole-document')
    r.ok('range-VERSION-equals-text-VERSION', g is not None and t is not None and len(g.f) > 2 and g.f[2] == t.f[2],
         'range reply %r, text reply %r' % (g and g.line, t and t.line))


plain('read.text-and-range', ['conf/full'], ops=READ_OPS,
      script=std(pre=['open {P}/a.txt', 'open {P}/utf.txt', 'open {P}/crlf.txt', OPEN_GO], wait=1800), check=chk_read)

# ================================================================ replace, insert, select

A_FINAL = 'alpha é€\U0001F600\nok 1 2 3\n0 bye\nBETA!\n'
REPLACE_STEPS = [
    ('text', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('replace-in-line', ["replace", "a.txt", "cur", 2, 1, 2, 5, "BETA!"], ('okv',)),
    ('stale-old-version', ["replace", "a.txt", "old", 1, 1, 1, 1, "X"], ('stale',)),
    ('badpos-line-beyond', ["replace", "a.txt", "cur", 9, 1, 9, 1, "Z"], ('err', 'badpos')),
    ('replace-no-such-document', ["replace", 424242, 1, 1, 1, 1, 1, "x"], ('err', 'nodoc')),
    ('replace-line-with-its-LF', ["replace", "a.txt", "cur", 3, 1, 4, 1, "G\n"], ('okv',)),
    ('replace-multibyte-insertion', ["replace", "a.txt", "cur", 1, 6, 1, 6, " é€\U0001F600"], ('okv',)),
    ('replace-body-that-looks-like-protocol', ["replace", "a.txt", "cur", 2, 1, 2, 1, "ok 1 2 3\n0 bye\n"], ('okv',)),
    ('replace-delete', ["replace", "a.txt", "cur", 5, 1, 6, 1, ""], ('okv',)),
    ('badpos-inside-character', ["replace", "utf.txt", "cur", 1, 2, 1, 2, "x"], ('err', 'badpos')),
    ('text-after', ["text", "a.txt"], ('body', A_FINAL.encode())),
    ["drain", 0.4],
    DONE,
]
REPLACE_OPS, REPLACE_CASES = split_steps(REPLACE_STEPS)


def chk_replace(r):
    r.finished()
    got = check_steps(r, REPLACE_STEPS)
    ids = dict(zip([c[0] for c in REPLACE_CASES], case_ids(REPLACE_STEPS)))
    v = {k: int(m.f[2]) for k, m in got.items() if m is not None and m.f[0] == 'ok' and len(m.f) > 2}
    seq = [v.get(k) for k in ('text', 'replace-in-line', 'replace-line-with-its-LF', 'replace-multibyte-insertion',
                              'replace-body-that-looks-like-protocol', 'replace-delete')]
    r.ok('versions-increase-with-every-change', None not in seq and all(a < b for a, b in zip(seq, seq[1:])),
         'versions %r' % seq)
    st = got.get('stale-old-version')
    r.eq('stale-text-is-the-current-version', st and st.line, 'error %d stale %s' % (ids['stale-old-version'], v.get('replace-in-line')))
    doc = r.docid('a.txt')
    ch = [int(m.f[3]) for m in r.events('changed') if m.f[2] == str(doc)]
    r.ok('changed-events-carry-versions', bool(ch) and ch == sorted(ch) and ch[-1] == v.get('replace-delete'),
         'changed versions %r, last NEWVERSION %r' % (ch, v.get('replace-delete')))
    r.eq('document-text', r.doc('d'), A_FINAL)
    r.eq('document-dirty', r.state('s').get('dirty'), '1')
    r.eq('rejected-edit-left-utf-untouched', r.doc('u'), UTF_TXT)


plain('edit.replace', ['conf/full'], ops=REPLACE_OPS,
      script=std(pre=['open {P}/utf.txt', OPEN_A], wait=1800, post=pd('d') + ps('s') + ['open {P}/utf.txt'] + pd('u')),
      check=chk_replace)


def chk_undo(r):
    r.finished()
    r.eq('replace-applied', r.doc('d1'), 'alpha\nBETA\ngamma\n')
    r.eq('one-undo-step-restores-the-text', r.doc('d2'), A_TXT)


plain('edit.replace-is-one-undo-step', ['conf/full'],
      ops=[["replace", "a.txt", "cur", 2, 1, 3, 1, "BETA\n"], DONE],
      script=std(pre=[OPEN_A], wait=1000, post=pd('d1') + ['cmd undo'] + pd('d2')), check=chk_undo)

CURSOR_OPS = [
    ["mark", "p0"], ["select", "a.txt", 2, 3, 2, 3], ["drain", 0.3], ["mark", "p1"],
    ["replace", "a.txt", "cur", 2, 1, 2, 5, "WXYZ12"], ["drain", 0.4], ["mark", "p2"],
    ["select", "a.txt", 1, 2, 1, 2], ["drain", 0.3], ["mark", "p3"],
    ["replace", "a.txt", "cur", 3, 1, 3, 3, "GG"], ["drain", 0.4], ["mark", "p4"], DONE]


def chk_cursor_move(r):
    r.finished()
    doc = r.docid('a.txt')

    def last(a, b, name):
        ev = r.between(a, b, name)
        return ev[-1].line if ev else None
    r.eq('select-places-the-cursor', last('p0', 'p1', 'cursor'), '0 cursor %d 2 3' % doc)
    r.eq('cursor-inside-the-range-moves-to-the-end-of-the-new-text', last('p1', 'p2', 'cursor'), '0 cursor %d 2 7' % doc)
    r.eq('select-again', last('p2', 'p3', 'cursor'), '0 cursor %d 1 2' % doc)
    others = [m.line for m in r.between('p3', 'p4', 'cursor') if m.line != '0 cursor %d 1 2' % doc]
    r.eq('cursor-outside-the-range-stays', others, [])
    s = r.state('s')
    r.ok('final-cursor', s.get('line') == '1' and s.get('col') == '2', 'print-state: %r' % s)
    r.eq('document-text', r.doc('d'), 'alpha\nWXYZ12\nGGmma\n')


plain('edit.replace-cursor', ['conf/full'], ops=CURSOR_OPS,
      script=std(pre=[OPEN_A], wait=2200, post=ps('s') + pd('d')), check=chk_cursor_move)

INSERT_STEPS = [
    ('select-two-bytes', ["select", "a.txt", 1, 1, 1, 3], ('ok',)),
    ('insert-replaces-the-selection', ["insert", "a.txt", "cur", "ZZ"], ('okv',)),
    ('place-cursor', ["select", "a.txt", 3, 1, 3, 1], ('ok',)),
    ('insert-at-the-cursor', ["insert", "a.txt", "cur", "<>"], ('okv',)),
    ('insert-stale', ["insert", "a.txt", "old", "no"], ('stale',)),
    ('insert-no-such-document', ["insert", 424242, 1, "x"], ('err', 'nodoc')),
    ('text-after', ["text", "a.txt"], ('body', b'ZZpha\nbeta\n<>gamma\n')),
    ["drain", 0.3],
    DONE,
]
INSERT_OPS, INSERT_CASES = split_steps(INSERT_STEPS)


def chk_insert(r):
    r.finished()
    got = check_steps(r, INSERT_STEPS)
    r.eq('document-text', r.doc('d'), 'ZZpha\nbeta\n<>gamma\n')
    v = [int(got[k].f[2]) for k in ('insert-replaces-the-selection', 'insert-at-the-cursor') if got.get(k)]
    r.ok('versions-increase', len(v) == 2 and v[0] < v[1], 'versions %r' % v)


plain('edit.insert', ['conf/full'], ops=INSERT_OPS, script=std(pre=[OPEN_A], wait=1500, post=pd('d')), check=chk_insert)

SELECT_STEPS = [
    ["mark", "q0"],
    ('select-forward', ["select", "a.txt", 1, 2, 2, 3], ('ok',)),
    ["drain", 0.3], ["mark", "q1"],
    ('select-backward', ["select", "a.txt", 2, 3, 1, 2], ('ok',)),
    ["drain", 0.3], ["mark", "q2"],
    ('select-equal-positions-places-the-cursor', ["select", "a.txt", 3, 2, 3, 2], ('ok',)),
    ["drain", 0.3], ["mark", "q3"],
    ('badpos-inside-character', ["select", "utf.txt", 1, 2, 1, 2], ('err', 'badpos')),
    ('badpos-line-beyond', ["select", "a.txt", 9, 9, 9, 9], ('err', 'badpos')),
    ('select-no-such-document', ["select", 424242, 1, 1, 1, 1], ('err', 'nodoc')),
    ('select-at-the-LF-position', ["select", "a.txt", 1, 6, 1, 6], ('ok',)),
    ('select-backward-final', ["select", "a.txt", 2, 3, 1, 2], ('ok',)),
    ["drain", 0.4],
    DONE,
]
SELECT_OPS, SELECT_CASES = split_steps(SELECT_STEPS)


def chk_select(r):
    r.finished()
    check_steps(r, SELECT_STEPS)
    doc = r.docid('a.txt')

    def lines(a, b, name):
        return [m.line for m in r.between(a, b, name)]
    r.ok('forward-selection-event', '0 selection %d 1 2 2 3' % doc in lines('q0', 'q1', 'selection'), 'got %r' % lines('q0', 'q1', 'selection'))
    r.ok('forward-cursor-at-L2-C2', lines('q0', 'q1', 'cursor')[-1:] == ['0 cursor %d 2 3' % doc], 'got %r' % lines('q0', 'q1', 'cursor'))
    r.ok('backward-selection-event-anchor-first', '0 selection %d 2 3 1 2' % doc in lines('q1', 'q2', 'selection'), 'got %r' % lines('q1', 'q2', 'selection'))
    r.ok('backward-cursor-at-L2-C2', lines('q1', 'q2', 'cursor')[-1:] == ['0 cursor %d 1 2' % doc], 'got %r' % lines('q1', 'q2', 'cursor'))
    r.ok('equal-positions-cursor-event', lines('q2', 'q3', 'cursor')[-1:] == ['0 cursor %d 3 2' % doc], 'got %r' % lines('q2', 'q3', 'cursor'))
    s = r.state('s')
    r.ok('final-cursor-is-the-second-position', s.get('line') == '1' and s.get('col') == '2', 'print-state: %r' % s)
    r.ok('final-selection-not-empty', s.get('sel') not in (None, '0'), 'print-state: %r' % s)


plain('edit.select', ['conf/full'], ops=SELECT_OPS,
      script=std(pre=['open {P}/utf.txt', OPEN_A], wait=2200, post=ps('s')), check=chk_select)


def chk_select_off(r):
    r.finished()
    s = r.state('s')
    r.eq('precondition-a-active', s.get('active'), 'a.txt')
    r.ok('selection-kept-for-when-the-document-is-shown', s.get('line') == '3' and s.get('col') == '3' and s.get('sel') not in (None, '0'),
         'print-state after activating a.txt: %r' % s)


plain('edit.select-hidden-document', ['conf/full'],
      ops=[["select", "a.txt", 3, 1, 3, 3], DONE],
      script=std(pre=[OPEN_A, OPEN_GO], wait=1000, post=['open {P}/a.txt'] + ps('s')), check=chk_select_off)


def chk_crlf_edit(r):
    r.finished()
    cases = check_steps(r, CRLF_EDIT_STEPS)
    with open(os.path.join(r.dirs['proj'], 'crlf.txt'), 'rb') as f:
        disk = f.read()
    r.eq('file-written-back-with-CRLF', disk, b'ONE\r\ntwo\r\n')
    doc = r.docid('crlf.txt')
    new = int(cases['replace'].f[2]) if cases.get('replace') else None
    sv = [m.line for m in r.events('saved')]
    r.eq('saved-event-carries-the-current-version', sv, ['0 saved %d %d' % (doc, new)])


CRLF_EDIT_STEPS = [
    ('replace', ["replace", "crlf.txt", "cur", 1, 1, 1, 4, "ONE"], ('okv',)),
    ["sleep", 1.2],
    ('text-after', ["text", "crlf.txt"], ('body', b'ONE\ntwo\n')),
    DONE,
]
CRLF_EDIT_OPS, _ = split_steps(CRLF_EDIT_STEPS)
plain('edit.CRLF-document', ['conf/full'], ops=CRLF_EDIT_OPS,
      script=['open {P}/crlf.txt', 'cmd full.go', 'wait-ext full running', 'wait 900', 'cmd save', 'wait 900', 'quit'],
      check=chk_crlf_edit)

# ================================================================ open


OPEN_STEPS = [
    ('open-absolute', ["open", 0, 0, "{PROJECT}/main.go"], ('okv',)),
    ["drain", 0.3],
    ('open-the-same-file-again', ["open", 0, 0, "{PROJECT}/main.go"], ('okv',)),
    ('open-relative-path-is-inside-the-project', ["open", 0, 0, "data.json"], ('okv',)),
    ('open-path-with-a-space', ["open", 0, 0, "{PROJECT}/with space.txt"], ('okv',)),
    ('open-at-a-position', ["open", 2, 3, "{PROJECT}/crlf.txt"], ('okv',)),
    ["drain", 0.3],
    ('nofile', ["open", 0, 0, "{PROJECT}/nothere.txt"], ('err', 'nofile')),
    ('image-is-not-a-text-document', ["open", 0, 0, "{PROJECT}/pic.png"], ('err', 'nodoc')),
    ["drain", 0.3],
    ('open-existing-tab-at-a-position', ["open", 2, 2, "{PROJECT}/a.txt"], ('okv',)),
    ["drain", 0.3],
    ('open-column-clamped-to-the-line', ["open", 2, 99, "{PROJECT}/utf.txt"], ('okv',)),
    ["drain", 0.3],
    ('open-new-file-position-clamped-to-the-document', ["open", 99, 99, "{PROJECT}/Makefile"], ('okv',)),
    ["drain", 0.3],
    DONE,
]
OPEN_OPS, OPEN_CASES = split_steps(OPEN_STEPS)


def chk_open(r):
    r.finished()
    got = check_steps(r, OPEN_STEPS)
    proj = r.dirs['proj']
    d = {k: int(m.f[2]) for k, m in got.items() if m is not None and m.f[0] == 'ok' and len(m.f) == 3}
    ev = r.lines()
    go = d.get('open-absolute')
    want = '0 opened %d' % (go or -1)
    mt = [l for l in ev if l.startswith(want + ' ')]
    r.ok('opened-event-follows-with-the-same-DOC', len(mt) == 1 and mt[0].endswith(' Go ' + os.path.join(proj, 'main.go')),
         'reply DOC %r, events %r' % (go, [l for l in ev if l.startswith('0 opened')]))
    r.eq('opening-an-open-file-gives-the-same-DOC', d.get('open-the-same-file-again'), go)
    r.ok('active-follows-the-open', ('0 active %d' % go) in ev if go else False, 'active events %r' % [l for l in ev if l.startswith('0 active')])
    sp = d.get('open-path-with-a-space')
    r.ok('path-with-a-space-arrives-whole', any(l.startswith('0 opened %d ' % sp) and l.endswith(' ' + os.path.join(proj, 'with space.txt')) for l in ev) if sp else False,
         'events %r' % [l for l in ev if 'space' in l])
    dj = d.get('open-relative-path-is-inside-the-project')
    r.ok('relative-path-opens-inside-the-project', any(l.startswith('0 opened %d ' % dj) and l.endswith(' ' + os.path.join(proj, 'data.json')) for l in ev) if dj else False,
         'events %r' % [l for l in ev if 'json' in l])
    pos = d.get('open-at-a-position')
    r.ok('cursor-at-the-requested-position', ('0 cursor %d 2 3' % pos) in ev if pos else False,
         'cursor events %r' % [l for l in ev if l.startswith('0 cursor')])
    a = r.docid('a.txt')
    r.eq('open-of-the-initial-tab-gives-its-DOC', d.get('open-existing-tab-at-a-position'), a)
    r.ok('existing-tab-gets-the-requested-position', ('0 cursor %d 2 2' % a) in ev if a else False,
         'cursor events %r' % [l for l in ev if l.startswith('0 cursor')])
    ut = d.get('open-column-clamped-to-the-line')
    r.ok('column-clamped-to-the-end-of-the-line', ('0 cursor %d 2 12' % ut) in ev if ut else False,
         'cursor events %r' % [l for l in ev if l.startswith('0 cursor')])
    mk = d.get('open-new-file-position-clamped-to-the-document')
    st = r.state('s')
    r.ok('position-clamped-to-the-end-of-the-document', st.get('active') == 'Makefile' and st.get('line') == '3' and st.get('col') == '1',
         'print-state after open 99 99 Makefile: %r' % st)
    r.ok('no-opened-event-for-the-image', not any('pic.png' in l for l in ev), 'events %r' % [l for l in ev if 'pic' in l])


plain('open.request', ['conf/full'], ops=OPEN_OPS, script=std(pre=[OPEN_A], wait=2800, post=ps('s')), check=chk_open)

# ================================================================ errors

UNKNOWN_STEPS = [
    ('unknown-verb', ["req", "frobnicate", "1 2"], ('err', 'unknown')),
    ('unknown-verb-no-args', ["req", "frobnicate"], ('err', 'unknown')),
    ('unknown-verb-again', ["req", "frobnicate", "x"], ('err', 'unknown')),
    ('unknown-verb-fourth', ["req", "frobnicate", "x y z"], ('err', 'unknown')),
    ('unknown-verb-fifth', ["req", "frobnicate", "x y z"], ('err', 'unknown')),
    ('still-served', ["text", "a.txt"], ('body', A_TXT.encode())),
    DONE,
]
U_OPS, U_CASES = split_steps(UNKNOWN_STEPS)


def chk_unknown(r):
    r.finished()
    check_steps(r, UNKNOWN_STEPS)
    e = r.ext('e', 'conf/full')
    r.ok('five-unknown-verbs-do-not-stop-the-extension', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))


plain('err.unknown-verb', ['conf/full'], ops=U_OPS, script=std(pre=[OPEN_A], wait=1000, post=pe('e')), check=chk_unknown)


NEEDS_STEPS = [
    ('text-needs-read', ["text", "a.txt"], ('errx', 'error %(id)d needs documents=read')),
    ('range-needs-read', ["range", "a.txt", 1, 1, 1, 3], ('errx', 'error %(id)d needs documents=read')),
    ('replace-needs-write', ["replace", "a.txt", "cur", 1, 1, 1, 3, "garbage 1\nmore garbage\nthird garbage\n"], ('errx', 'error %(id)d needs documents=write')),
    ('insert-needs-write', ["insert", "a.txt", "cur", "x y z\nq\nw\n"], ('errx', 'error %(id)d needs documents=write')),
    ('select-needs-nothing', ["select", "a.txt", 1, 1, 1, 2], ('ok',)),
    ('toast-needs-nothing', ["toast", "hi"], ('ok',)),
    ('status-needs-nothing', ["status", "st"], ('ok',)),
    ('cancel-needs-nothing', ["cancel", 999], ('ok',)),
    ('open-closed-file-needs-disk-read', ["open", 0, 0, "{PROJECT}/main.go"], ('errx', 'error %(id)d needs file=read')),
    ('open-existing-tab-needs-nothing', ["open", 0, 0, "{PROJECT}/a.txt"], ('okv',)),
    DONE,
]
N_OPS, N_CASES = split_steps(NEEDS_STEPS)


def chk_needs(r):
    r.finished()
    check_steps(r, NEEDS_STEPS)
    e = r.ext('e', 'conf/nocap')
    r.ok('bodies-of-refused-requests-are-skipped', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))
    r.eq('nothing-happened-to-the-document', r.doc('d'), A_TXT)


plain('err.needs-no-capability', ['conf/nocap'], ops=N_OPS,
      script=['open {P}/a.txt', 'cmd nocap.go', 'wait-ext nocap running', 'wait 1500'] + pe('e') + ['open {P}/a.txt'] + pd('d') + ['quit'],
      check=chk_needs)

NEEDS_RD_STEPS = [
    ('text-allowed', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('range-allowed', ["range", "a.txt", 1, 1, 1, 3], ('body', b'al')),
    ('replace-needs-write', ["replace", "a.txt", "cur", 1, 1, 1, 3, "garbage 1\nmore garbage\nthird garbage\n"], ('errx', 'error %(id)d needs documents=write')),
    ('insert-needs-write', ["insert", "a.txt", "cur", "x y z\nq\nw\n"], ('errx', 'error %(id)d needs documents=write')),
    ('still-served', ["text", "a.txt"], ('body', A_TXT.encode())),
    DONE,
]
NR_OPS, NR_CASES = split_steps(NEEDS_RD_STEPS)


def chk_needs_rd(r):
    r.finished()
    check_steps(r, NEEDS_RD_STEPS)
    r.eq('nothing-happened-to-the-document', r.doc('d'), A_TXT)


plain('err.needs-read-only', ['conf/rd'], ops=NR_OPS,
      script=['open {P}/a.txt', 'cmd rd.go', 'wait-ext rd running', 'wait 1500'] + pd('d') + ['quit'], check=chk_needs_rd)

SYNTAX_STEPS = [
    ('text-non-numeric-DOC', ["req", "text", "abc"], ('err', 'syntax')),
    ('ok-1', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('range-too-few-arguments', ["req", "range", "1 1"], ('err', 'syntax')),
    ('ok-2', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('text-no-arguments', ["req", "text"], ('err', 'syntax')),
    ('ok-3', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('toast-bad-length', ["req", "toast", "abc"], ('err', 'syntax')),
    ('ok-4', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('select-too-few-arguments', ["req", "select", "1 2"], ('err', 'syntax')),
    ('ok-5', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('open-non-numeric-position', ["req", "open", "x y z"], ('err', 'syntax')),
    ('ok-6', ["text", "a.txt"], ('body', A_TXT.encode())),
    DONE,
]
SY_OPS, SY_CASES = split_steps(SYNTAX_STEPS)


def chk_syntax_args(r):
    r.finished()
    check_steps(r, SYNTAX_STEPS)
    e = r.ext('e', 'conf/full')
    r.ok('well-formed-requests-between-keep-it-running', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))


plain('err.syntax-in-arguments', ['conf/full'], ops=SY_OPS, script=std(pre=[OPEN_A], wait=1200, post=pe('e')), check=chk_syntax_args)

CANCEL_STEPS = [('cancel-unknown-id', ["cancel", 12345], ('ok',)), ('cancel-0', ["cancel", 0], ('ok',)), DONE]


def chk_cancel(r):
    r.finished()
    check_steps(r, CANCEL_STEPS)


plain('err.cancel-always-ok', ['conf/full'], ops=split_steps(CANCEL_STEPS)[0], script=std(wait=700), check=chk_cancel)

# ================================================================ protocol 1.1: tests

ITEM_G = "0 1\ngroup 0 0 0 0 - 1 1 0 0\ngG"
TESTS_STEPS = [
    ('items-replace', ["req", "test-items", "replace {LEN}", ITEM_G], ('ok',)),
    ('items-unknown-parent', ["req", "test-items", "add {LEN}", "4 1\nnopetest 0 0 0 0 - 1 0 0 0\nx"], ('errx', 'error %(id)d refused unknown parent')),
    ('items-bad-kind', ["req", "test-items", "add {LEN}", "0 1\nfunc 0 0 0 0 - 1 0 0 0\nx"], ('err', 'syntax')),
    ('items-bad-range', ["req", "test-items", "add {LEN}", "0 1\ntest 3 5 3 4 - 1 0 2 0\nx/a"], ('err', 'badpos')),
    ('items-range-without-path', ["req", "test-items", "add {LEN}", "0 1\ntest 1 1 1 1 - 1 0 0 0\nx"], ('err', 'badpos')),
    ('items-10001', ["req", "test-items", "add {LEN}", {'pre': '0 10001\n', 'rep': 'test 0 0 0 0 - 1 0 0 0\nx', 'n': 10001}], ('err', 'toobig')),
    ('items-add-under-g', ["req", "test-items", "add {LEN}", "1 1\ngtest 0 0 0 0 - 3 1 0 0\ng/aA"], ('ok',)),
    ('remove', ["req", "test-remove", "2 {LEN}", "g/a\nghost\n"], ('ok',)),
    ('update-unknown-run', ["req", "test-update", "999 {LEN}", "0\n"], ('errx', 'error %(id)d stale run over')),
    ('end-unknown-run', ["req", "test-end", "999"], ('errx', 'error %(id)d stale run over')),
    ('coverage-unknown-run', ["req", "test-coverage", "999 {LEN}", "0\n"], ('errx', 'error %(id)d stale run over')),
    ["waitfor", " test-run 1 ", 15],
    ('exec-not-needed', ["req", "test-exec", "1 {LEN}", "2 0 0 0\nls"], ('errx', 'error %(id)d needs tests=ls')),
    ('exec-not-bare', ["req", "test-exec", "1 {LEN}", "4 0 0 0\n../x"], ('errx', 'error %(id)d refused the program must be a bare name')),
    ('exec-nul', ["req", "test-exec", "1 {LEN}", "3 0 0 0\ngo\x00"], ('err', 'syntax')),
    ('update', ["req", "test-update", "1 {LEN}", "1\npassed 4 1 0\ng"], ('ok',)),
    ('update-bad-state', ["req", "test-update", "1 {LEN}", "1\nbroken - 1 0\ng"], ('err', 'syntax')),
    ('end', ["req", "test-end", "1"], ('ok',)),
    ('update-after-end', ["req", "test-update", "1 {LEN}", "0\n"], ('errx', 'error %(id)d stale run over')),
    DONE,
]
TESTS_OPS, TESTS_CASES = split_steps(TESTS_STEPS)


def chk_tests_requests(r):
    r.finished()
    check_steps(r, TESTS_STEPS)
    r.ok('tests-resolve-for-the-roots', any(' tests-resolve 0' in l for l in r.lines()), 'rhun never asked for the roots')
    r.ok('test-run-sent', any(' test-run 1 run 0 0' in l for l in r.lines()), 'no test-run 1 run 0 0')
    e = r.ext('e', 'conf/tests')
    r.ok('still-running', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))


plain('tests.requests', ['conf/tests'], ops=TESTS_OPS, env={'CONF_HELLO': 'hello 1 tests\\n'},
      script=['cmd testing', 'wait-ext tests running', 'wait 1200', 'cmd test_run_all', 'wait 2500'] + pe('e') + ['quit'],
      check=chk_tests_requests)

# ================================================================ 1.1: editor presentation

PRES_STEPS = [
    ('type', ["req", "decoration-type", "blame {LEN}", "after muted -\ngrow none\n"], ('ok',)),
    ('type-unknown-key', ["req", "decoration-type", "bad {LEN}", "faded\nwobble 3\n"], ('errx', 'error %(id)d syntax line 2')),
    ('type-bad-name', ["req", "decoration-type", "Bad! {LEN}", "faded\n"], ('err', 'syntax')),
    ('decorate', ["req", "decorate", "{DOC:a.txt} 0 blame 0 0 1 {LEN}", "1 1 1 3 0 3 0 0\nyou"], ('ok',)),
    ('decorate-unknown-type', ["req", "decorate", "{DOC:a.txt} 0 nosuch 0 0 0 {LEN}", ""], ('err', 'syntax')),
    ('decorate-outside-the-text', ["req", "decorate", "{DOC:a.txt} 0 blame 0 0 1 {LEN}", "99 1 99 2 0 0 0 0\n"], ('err', 'badpos')),
    ('decorate-fewer-records', ["req", "decorate", "{DOC:a.txt} 0 blame 0 0 2 {LEN}", "1 1 1 3 0 0 0 0\n"], ('err', 'syntax')),
    ('decorate-no-such-document', ["req", "decorate", "424242 0 blame 0 0 0 {LEN}", ""], ('err', 'nodoc')),
    ('legend', ["req", "semantic-legend", "ra {LEN}", "function variable=builtin\nmutable=underline"], ('ok',)),
    ('legend-unknown-effect', ["req", "semantic-legend", "rb {LEN}", "function=sparkle\n"], ('err', 'syntax')),
    DONE,
]
PRES_OPS, PRES_CASES = split_steps(PRES_STEPS)


def chk_presentation(r):
    r.finished()
    check_steps(r, PRES_STEPS)
    e = r.ext('e', 'conf/full')
    r.ok('still-running', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))


plain('pres.decorations-and-legends', ['conf/full'], ops=PRES_OPS,
      env={'CONF_HELLO': 'hello 1 decorations semantic-tokens\\n'},
      script=std(pre=[OPEN_A], wait=1200, post=pe('e')), check=chk_presentation)


def chk_presentation_unagreed(r):
    r.finished()
    check_cases(r, [('decoration-type-unknown', ('err', 'unknown')), ('semantic-legend-unknown', ('err', 'unknown'))])


plain('pres.without-the-features-unknown', ['conf/full'],
      ops=[["req", "decoration-type", "t {LEN}", "faded\n"], ["req", "semantic-legend", "ra {LEN}", "function\n"], DONE],
      script=std(pre=[OPEN_A], wait=900), check=chk_presentation_unagreed)


# ================================================================ protocol 1.1: terminals and tasks

def tdef(task_id, form, label, cmd, extra=''):
    """A definition: TASK_ID FORM LABEL_LEN CMD_LEN CWD_LEN EXTRA_LEN LF + the fields."""
    return '%s %s %d %d 0 %d\n%s%s%s' % (task_id, form, len(label.encode()), len(cmd.encode()),
                                          len(extra.encode()), label, cmd, extra)


def trec(name, args='', data=''):
    """An EXTRA record: NAME ARGS LEN LF + its bytes."""
    return '%s%s %d\n%s' % (name, (' ' + args) if args else '', len(data.encode()), data)


def tlist(*defs):
    return '%d\n%s' % (len(defs), ''.join(defs))


TERM_ENV = {'CONF_HELLO': 'hello 1 terminals tasks\\n', 'TERM': 'xterm-256color'}
ECHO_DEF = tdef('-', 'shell', 'Hi', 'echo conf-term-ran; exit 3')
TERM_IDS = 'error %(id)d needs terminal='

LADDER_STEPS = [
    ('open-needs-run', ["req", "terminal-open", "{LEN}", ECHO_DEF], ('errx', TERM_IDS + 'run')),
    ('stop-needs-run', ["req", "terminal-stop", "1"], ('errx', TERM_IDS + 'run')),
    ('tasks-set-needs-run', ["req", "tasks-set", "{LEN}", "0\n"], ('errx', TERM_IDS + 'run')),
    ('task-start-needs-run', ["req", "task-start", "build"], ('errx', TERM_IDS + 'run')),
    DONE,
]
LADDER_OPS, _ = split_steps(LADDER_STEPS)


def chk_terminals_ladder(r):
    r.finished()
    check_steps(r, LADDER_STEPS)
    r.ok('features-agreed', '0 features terminals tasks' in r.lines(), 'got %r' % [l for l in r.lines() if 'features' in l])
    r.ok('no-dialog', 'dialog=none' in r.seg('d'), 'got %r' % r.seg('d'))


plain('terminals.ladder-suggest', ['conf/tsug'], ops=LADDER_OPS, env=TERM_ENV,
      script=['wait-ext tsug running', 'wait 1500', 'echo @d', 'print-dialog', 'quit'], check=chk_terminals_ladder)

UNAGREED_STEPS = [
    ('tasks-set-unagreed', ["req", "tasks-set", "{LEN}", "0\n"], ('err', 'unknown')),
    DONE,
]
UNAGREED_OPS, _ = split_steps(UNAGREED_STEPS)


def chk_tasks_unagreed(r):
    r.finished()
    check_steps(r, UNAGREED_STEPS)
    feats = [l for l in r.lines() if l.startswith('0 features')]
    r.ok('tasks-only-with-terminals', all('tasks' not in l.split()[2:] for l in feats), 'got %r' % feats)


plain('tasks.only-with-terminals', ['conf/trun'], ops=UNAGREED_OPS,
      env={'CONF_HELLO': 'hello 1 tasks\\n', 'TERM': 'xterm-256color'},
      script=['wait-ext trun running', 'wait 1000', 'quit'], check=chk_tasks_unagreed)

TOPEN_STEPS = [
    ('open', ["req", "terminal-open", "{LEN}", ECHO_DEF], ('errx', 'ok %(id)d 1')),
    ["waitfor", "0 terminal-ended 1 ", 10],
    ('open-argv', ["req", "terminal-open", "{LEN}", tdef('-', 'argv', 'Args', 'sh\x00-c\x00echo "[$0] [$1]"\x00zero\x00one two\x00')],
     ('errx', 'ok %(id)d 2')),
    ["waitfor", "0 terminal-ended 2 ", 10],
    ('input-needs-input', ["req", "terminal-input", "1 {LEN}", "x\n"], ('errx', TERM_IDS + 'input')),
    ('screen-needs-output', ["req", "terminal-screen", "1"], ('errx', TERM_IDS + 'output')),
    ('output-record-needs-output', ["req", "terminal-open", "{LEN}", tdef('-', 'shell', 'O', 'true', trec('output'))],
     ('errx', TERM_IDS + 'output')),
    ('noterm', ["req", "terminal-stop", "999"], ('errx', 'error %(id)d noterm no such terminal')),
    ('task-record-in-terminal', ["req", "terminal-open", "{LEN}", tdef('-', 'shell', 'G', 'true', trec('group', 'build'))],
     ('errx', 'error %(id)d syntax definition: not in a terminal: group, default, depends, matcher, ready, busy')),
    ('terminal-id-is-dash', ["req", "terminal-open", "{LEN}", tdef('x', 'shell', 'X', 'true')],
     ('errx', "error %(id)d syntax definition: id: a terminal's is -")),
    ('unsupported-location', ["req", "terminal-open", "{LEN}", tdef('-', 'shell', 'L', 'true', trec('location', 'remote'))],
     ('errx', 'error %(id)d syntax definition: unsupported location')),
    ('unknown-record-skipped', ["req", "terminal-open", "{LEN}", tdef('-', 'shell', 'U', 'true', trec('future', 'a b'))],
     ('errx', 'ok %(id)d 3')),
    ('show', ["req", "terminal-show", "1 focus"], ('ok',)),
    ('restart', ["req", "terminal-restart", "1"], ('ok',)),
    ["waitfor", "0 terminal-ended 1 ", 10],
    ('close', ["req", "terminal-close", "1"], ('ok',)),    # terminal-closed may come before the reply
    ["drain", 0.3],
    ('closed-is-noterm', ["req", "terminal-stop", "1"], ('errx', 'error %(id)d noterm no such terminal')),
    DONE,
]
TOPEN_OPS, _ = split_steps(TOPEN_STEPS)


def chk_terminals_open(r):
    r.finished()
    check_steps(r, TOPEN_STEPS)
    lines = r.lines()
    r.ok('started-event', any(re.match(r'^0 terminal-started 1 [1-9]\d* -$', l) for l in lines), 'no terminal-started 1 PID -')
    r.eq('ended-twice-with-the-code', [l for l in lines if l.startswith('0 terminal-ended 1 ')],
         ['0 terminal-ended 1 exit 3 0 0'] * 2)
    r.ok('restarted', len([l for l in lines if l.startswith('0 terminal-started 1 ')]) == 2, 'one start of TERM 1')
    r.ok('closed-event', '0 terminal-closed 1' in lines, 'no terminal-closed 1')
    r.ok('asked', any(l.startswith('dialog=Let ') for l in r.seg('d1')), 'got %r' % r.seg('d1'))
    r.ok('argv-as-given', any('[zero] [one two]' in l for l in r.seg('t2')), 'got %r' % r.seg('t2'))


plain('terminals.open', ['conf/trun'], ops=TOPEN_OPS, env=TERM_ENV,
      script=['wait-ext trun running', 'wait 1200', 'echo @d1', 'print-dialog', 'dialog yes', 'wait 6000',
              'echo @t2', 'term-select 2', 'print-term', 'quit'], check=chk_terminals_open)

TIO_STEPS = [
    ('open-cat', ["req", "terminal-open", "{LEN}", tdef('-', 'argv', 'Cat', 'cat\x00', trec('output'))], ('errx', 'ok %(id)d 1')),
    ["waitfor", "0 terminal-started 1 ", 10],
    ('input', ["req", "terminal-input", "1 {LEN}", "conf-typed\n"], ('ok',)),
    ["waitfor", "0 terminal-output 1 ", 10],
    ["sleep", 0.3],
    ('screen', ["req", "terminal-screen", "1"], ('okv',)),
    ('close', ["req", "terminal-close", "1"], ('ok',)),    # terminal-closed may come before the reply
    ["drain", 0.3],
    DONE,
]
TIO_OPS, _ = split_steps(TIO_STEPS)


def chk_terminals_io(r):
    r.finished()
    check_steps(r, TIO_STEPS)
    outs = [m for m in r.events('terminal-output') if m.f[2] == '1']
    r.ok('output-lines', any(m.body is not None and b'conf-typed\n' in m.body for m in outs),
         'got %r' % [(m.line, m.body) for m in outs])
    r.ok('output-dropped-field', all(re.match(r'^0 terminal-output 1 \d+ \d+$', m.line) for m in outs), 'got %r' % [m.line for m in outs])
    ids = case_ids(TIO_STEPS)
    m = r.reply(ids[[lbl for lbl, _ in split_steps(TIO_STEPS)[1]].index('screen')])
    body = (m.body or b'').decode('utf-8', 'replace') if m else ''
    head = body.split('\n', 1)[0]
    r.ok('screen-body', re.match(r'^\d+ \d+ \d+ \d+$', head) is not None and 'conf-typed' in body, 'got %r' % body[:200])


plain('terminals.input-output', ['conf/tout'], ops=TIO_OPS, env=TERM_ENV,
      script=['wait-ext tout running', 'wait 1200', 'dialog yes', 'wait 4000', 'quit'], check=chk_terminals_io)


def chk_terminals_input_cap(r):
    e = r.ext('e', 'conf/tout')
    r.ok('stopped', e is not None and e['state'] == 'stopped', 'got %r' % (e and e['raw']))
    r.ok('reason', e is not None and 'a body over its cap' in (e['reason'] or ''), 'got %r' % (e and e['raw']))


# terminal-input's body is at most 64 KiB: one over it cannot be skipped unread, so the program ends
plain('terminals.input-over-its-cap', ['conf/tout'], ops=[["req", "terminal-input", "1 {LEN}", {'rep': 'x', 'n': 65537}]],
      env=TERM_ENV, script=['wait-ext tout running', 'wait 2000'] + pe('e') + ['quit'], check=chk_terminals_input_cap)

GOOD_LIST = tlist(
    tdef('lint', 'shell', 'Lint', 'echo lint-ok'),
    tdef('build', 'shell', 'Build', 'echo building', trec('group', 'build') + trec('default') + trec('matcher', 'go')),
    tdef('chain', 'shell', 'Chain', 'echo chain-done', trec('depends', 'sequence', 'lint build')),
    tdef('slow', 'shell', 'Slow', 'sleep 30'),
    tdef('serve', 'shell', 'Serve', "echo starting; sleep 0.3; echo 'Serving HTTP on 0.0.0.0'; sleep 30",
         trec('ready', '', 'Serving HTTP on {_}')),
    tdef('smoke', 'shell', 'Smoke', 'echo smoke-ran', trec('depends', 'sequence', 'serve')),
)
T1 = lambda *x: tlist(*x)
TASKS_SET_STEPS = [
    ('count', ["req", "tasks-set", "{LEN}", "x\n"], ('errx', 'error %(id)d syntax the count of tasks')),
    ('twice', ["req", "tasks-set", "{LEN}", T1(tdef('a', 'shell', 'A', 'true'), tdef('a', 'shell', 'A2', 'true'))],
     ('errx', 'error %(id)d syntax task 1: id listed twice')),
    ('depends-unknown', ["req", "tasks-set", "{LEN}", T1(tdef('a', 'shell', 'A', 'true', trec('depends', 'sequence', 'nope')))],
     ('errx', 'error %(id)d syntax task 1: depends: no task "nope"')),
    ('cycle', ["req", "tasks-set", "{LEN}", T1(tdef('a', 'shell', 'A', 'true', trec('depends', 'sequence', 'b')),
                                                 tdef('b', 'shell', 'B', 'true', trec('depends', 'parallel', 'a')))],
     ('err', 'syntax')),
    ('bad-pattern', ["req", "tasks-set", "{LEN}", T1(tdef('a', 'shell', 'A', 'true', trec('ready', '', '{nope}')))],
     ('errx', 'error %(id)d syntax task 1: unknown field')),
    ('busy-without-ready', ["req", "tasks-set", "{LEN}", T1(tdef('a', 'shell', 'A', 'true', trec('busy', '', 'x')))],
     ('errx', 'error %(id)d syntax task 1: busy needs ready')),
    ('unknown-matcher', ["req", "tasks-set", "{LEN}", T1(tdef('a', 'shell', 'A', 'true', trec('matcher', 'javac')))],
     ('errx', 'error %(id)d syntax task 1: matcher: go, gcc, rustc, tsc or msbuild')),
    ('bad-id', ["req", "tasks-set", "{LEN}", T1(tdef('Bad', 'shell', 'A', 'true'))],
     ('errx', 'error %(id)d syntax task 1: id: 1 to 64 bytes of a-z 0-9 _ . -, from a letter or digit')),
    ('output-needs-output', ["req", "tasks-set", "{LEN}", T1(tdef('a', 'shell', 'A', 'true', trec('output')))],
     ('errx', TERM_IDS + 'output')),
    ('trailing', ["req", "tasks-set", "{LEN}", "0\nx"], ('errx', 'error %(id)d syntax bytes after the last definition')),
    ('set', ["req", "tasks-set", "{LEN}", GOOD_LIST], ('ok',)),
    ('start-unknown', ["req", "task-start", "nothing-here"], ('errx', 'error %(id)d notask no such published task')),
    ('start-chain', ["req", "task-start", "chain"], ('errx', 'ok %(id)d 3')),
    ["waitfor", "0 terminal-ended 3 ", 10],
    ('run-with-a-published-dependency', ["req", "task-run", "{LEN}",
                                         tdef('adhoc', 'shell', 'Adhoc', 'echo adhoc-ran', trec('depends', 'sequence', 'lint'))],
     ('errx', 'ok %(id)d 4')),
    ["waitfor", "0 terminal-ended 4 ", 10],
    ('run-unpublished-dependency', ["req", "task-run", "{LEN}", tdef('x', 'shell', 'X', 'true', trec('depends', 'sequence', 'ghost'))],
     ('errx', 'error %(id)d syntax definition: depends: names a task that is not published')),
    ('start-slow', ["req", "task-start", "slow"], ('errx', 'ok %(id)d 5')),
    ["waitfor", "0 terminal-started 5 ", 10],
    ('start-running-gives-its-term', ["req", "task-start", "slow"], ('errx', 'ok %(id)d 5')),
    ('stop-slow', ["req", "terminal-stop", "5"], ('ok',)),
    ["waitfor", "0 terminal-ended 5 ", 10],
    ('start-smoke', ["req", "task-start", "smoke"], ('errx', 'ok %(id)d 7')),
    ["waitfor", "0 terminal-ended 7 ", 10],
    DONE,
]
TASKS_SET_OPS, _ = split_steps(TASKS_SET_STEPS)


def chk_tasks_requests(r):
    r.finished()
    check_steps(r, TASKS_SET_STEPS)
    lines = r.lines()
    started = [l.split()[4] for l in lines if l.startswith('0 terminal-started ')]
    r.eq('dependencies-first', started[:5], ['lint', 'build', 'chain', 'lint', 'adhoc'])
    r.ok('chain-done', '0 terminal-ended 3 exit 0 0 0' in lines, 'got %r' % [l for l in lines if 'ended 3 ' in l])
    r.ok('adhoc-done', '0 terminal-ended 4 exit 0 0 0' in lines, 'got %r' % [l for l in lines if 'ended 4 ' in l])
    r.ok('cycle-named', any('depends: a cycle' in l for l in lines), 'no "depends: a cycle"')
    r.ok('stopped', any(re.match(r'^0 terminal-ended 5 stopped ', l) for l in lines), 'got %r' % [l for l in lines if 'ended 5 ' in l])
    r.ok('ready-before-dependent', '0 task-ready 6 0 0' in lines and
         lines.index('0 task-ready 6 0 0') < next((i for i, l in enumerate(lines) if l.startswith('0 terminal-started 7 ')), -1),
         'got %r' % [l for l in lines if ' 6 ' in l or ' 7 ' in l])
    r.ok('smoke-done', '0 terminal-ended 7 exit 0 0 0' in lines, 'got %r' % [l for l in lines if 'ended 7 ' in l])


plain('tasks.requests-and-runs', ['conf/trun'], ops=TASKS_SET_OPS, env=TERM_ENV,
      script=['wait-ext trun running', 'wait 1500', 'dialog yes', 'wait 12000', 'quit'], check=chk_tasks_requests,
      timeout=90)

CANCEL_TASK_STEPS = [
    ('set', ["req", "tasks-set", "{LEN}", T1(tdef('build', 'shell', 'Build', 'echo building'))], ('ok',)),
    ('start-cancelled', ["req", "task-start", "build", {'nowait': True}], ('errx', 'error %(id)d cancelled')),
    ["sleep", 1.5],
    ('cancel-start', ["cancel", 2], ('ok',)),
    ('open-cancelled', ["req", "terminal-open", "{LEN}", ECHO_DEF, {'nowait': True}], ('errx', 'error %(id)d cancelled')),
    ["sleep", 1.5],
    ('cancel-open', ["cancel", 4], ('ok',)),
    ["sleep", 0.3],
    DONE,
]
CANCEL_TASK_OPS, _ = split_steps(CANCEL_TASK_STEPS)


def chk_tasks_cancel(r):
    r.finished()
    check_steps(r, CANCEL_TASK_STEPS)
    r.ok('asked', any(l.startswith('dialog=Let ') for l in r.seg('d1')), 'got %r' % r.seg('d1'))
    r.ok('dialog-closed', 'dialog=none' in r.seg('d2'), 'got %r' % r.seg('d2'))
    r.ok('nothing-allowed', not any(l.startswith('conf/trun ') for l in r.seg('a')), 'got %r' % r.seg('a'))
    r.ok('nothing-ran', not r.events('terminal-started'), 'got %r' % [m.line for m in r.events('terminal-started')])


plain('tasks.cancel-during-the-question', ['conf/trun'], ops=CANCEL_TASK_OPS, env=TERM_ENV,
      script=['wait-ext trun running', 'wait 1000', 'echo @d1', 'print-dialog', 'wait 4000', 'echo @d2', 'print-dialog',
              'echo @a', 'print-allowed', 'quit'], check=chk_tasks_cancel)

# ================================================================ section 7: status and toast


def status_scn(name, body_op, want):
    def chk(r):
        r.finished()
        e = r.ext('s', 'conf/full')
        r.ok('status-text', e is not None and e['status'] == want, 'got %r, want status=%r' % (e and e['raw'], want))
        r.ok('extension-still-running', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))
    plain('ui.status-' + name, ['conf/full'], ops=body_op + [["sleep", 0.2], DONE],
          script=std(wait=900, post=pe('s')), check=chk)


status_scn('plain', [["status", "building 3 of 7"]], 'building 3 of 7')
status_scn('exactly-80-bytes', [["status", {"rep": "x", "n": 80}]], 'x' * 80)
status_scn('over-80-bytes-is-cut', [["status", {"rep": "x", "n": 100}]], 'x' * 80)
status_scn('cut-at-a-character-boundary-2-byte', [["status", {"pre": "a", "rep": "é", "n": 50}]], 'a' + 'é' * 39)
status_scn('cut-lands-exactly-between-2-byte-characters', [["status", {"rep": "é", "n": 45}]], 'é' * 40)
status_scn('cut-at-a-character-boundary-3-byte', [["status", {"pre": "a", "rep": "€", "n": 40}]], 'a' + '€' * 26)
status_scn('cut-at-a-character-boundary-4-byte', [["status", {"pre": "a", "rep": "\U0001F600", "n": 30}]], 'a' + '\U0001F600' * 19)
status_scn('empty-body-clears', [["status", "temporary"], ["status", ""]], None)
status_scn('replaced-by-the-next', [["status", "first"], ["status", "second"]], 'second')


def chk_status_stop(r):
    b, s = r.ext('b', 'conf/full'), r.ext('s', 'conf/full')
    r.ok('status-visible-while-running', b is not None and b['status'] == 'will be removed', 'got %r' % (b and b['raw']))
    r.ok('stopped-by-three-syntax-errors', s is not None and s['state'] == 'stopped', 'got %r' % (s and s['raw']))
    r.ok('status-removed-when-the-extension-stops', s is not None and s['status'] is None, 'got %r' % (s and s['raw']))


plain('ui.status-removed-on-stop', ['conf/full'],
      ops=[["status", "will be removed"], ["sleep", 1.2], ["raw", "garbage one\n"], ["raw", "garbage two\n"],
           ["raw", "garbage three\n"], ["sleep", 5]],
      script=['cmd full.go', 'wait 1000'] + pe('b') + ['wait 2500'] + pe('s') + ['quit'], check=chk_status_stop)


TOAST_STEPS = [
    ('toast', ["toast", "hello"], ('ok',)),
    ('toast-empty', ["toast", ""], ('ok',)),
    ('toast-300-bytes-is-cut-not-fatal', ["toast", {"rep": "y", "n": 300}], ('ok',)),
    ('toast-multibyte-over-limit', ["toast", {"rep": "\u00e9", "n": 500}], ('ok',)),
    ('status-300-bytes-is-cut-not-fatal', ["status", {"rep": "y", "n": 300}], ('ok',)),
    ('after', ["text", "a.txt"], ('body', A_TXT.encode())),
    DONE,
]


def chk_toast_big(r):
    r.finished()
    check_steps(r, TOAST_STEPS)
    e = r.ext('s', 'conf/full')
    r.ok('over-limit-bodies-are-truncated-not-fatal', e is not None and e['state'] == 'running' and e['status'] == 'y' * 80,
         'got %r' % (e and e['raw']))


plain('ui.toast-and-status-bodies', ['conf/full'], ops=split_steps(TOAST_STEPS)[0],
      script=std(pre=[OPEN_A], wait=1500, post=pe('s')), check=chk_toast_big)


def big_body_scn(verb):
    """A 100 KB body for a verb whose table row says "truncated" (section 7)."""
    def chk(r):
        rep = r.reply(1)
        r.ok('reply-ok', rep is not None and rep.line == 'ok 1',
             'got %r (an over-limit toast/status body is truncated, not fatal)' % (rep and rep.line))
        e = r.ext('s', 'conf/full')
        r.ok('extension-still-running', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))
    plain('ui.%s-body-100KB-is-truncated' % verb, ['conf/full'], ops=[[verb, {"rep": "y", "n": 100000}], DONE],
          script=std(pre=[OPEN_A], wait=1200, post=pe('s')), check=chk)


big_body_scn('toast')
big_body_scn('status')

# ================================================================ section 4.5-4.6: crashes, stops


def chk_crash(r):
    c = [r.ext('c%d' % i, 'conf/full') for i in (1, 2, 3, 4)]
    raw = lambda x: x and x['raw']
    r.ok('exit-after-hello-is-crashed', c[0] is not None and c[0]['state'] == 'crashed', 'got %r' % raw(c[0]))
    r.ok('crash-reason-has-the-exit-status', c[0] is not None and c[0]['reason'] is not None and '3' in c[0]['reason'],
         'got %r, want a reason that names exit status 3' % raw(c[0]))
    r.ok('commands-stay-after-a-crash', c[0] is not None and c[0]['cmds'] == 'full.go,full.go2', 'got %r' % raw(c[0]))
    r.ok('command-starts-it-again', c[1] is not None and c[1]['state'] == 'crashed' and True, 'got %r' % raw(c[1]))
    r.ok('third-crash-within-a-minute-leaves-it-stopped', c[2] is not None and c[2]['state'] == 'stopped', 'got %r' % raw(c[2]))
    r.ok('stopped-has-a-reason', c[2] is not None and bool(c[2]['reason']), 'got %r' % raw(c[2]))
    r.ok('stopped-ignores-commands', c[3] is not None and c[3]['state'] == 'stopped', 'got %r' % raw(c[3]))
    r.eq('processes-started', r.hello_count(), 3)


plain('life.three-crashes', ['conf/full'], env={'CONF_EXIT_AFTER_HELLO': '3'},
      script=sum([['cmd full.go', 'wait 700'] + pe('c%d' % i) for i in (1, 2, 3, 4)], []) + ['quit'], check=chk_crash)


def chk_two_crashes_restart(r):
    c = [r.ext('c%d' % i, 'conf/full') for i in (1, 2)]
    r.ok('crashed-after-first-start', c[0] is not None and c[0]['state'] == 'crashed', 'got %r' % (c[0] and c[0]['raw']))
    r.ok('crashed-again-after-second-start', c[1] is not None and c[1]['state'] == 'crashed', 'got %r' % (c[1] and c[1]['raw']))
    r.eq('second-command-started-a-new-process', r.hello_count(), 2)


plain('life.crash-then-command-restarts', ['conf/full'], env={'CONF_EXIT_AFTER_HELLO': '0'},
      script=sum([['cmd full.go', 'wait 700'] + pe('c%d' % i) for i in (1, 2)], []) + ['quit'], check=chk_two_crashes_restart)


def limit_check(r, reason=None, within=None, since=None, doc=A_TXT):
    """Common checks for an extension that a limit stopped."""
    e = r.ext('e', 'conf/full')
    r.ok('stopped', e is not None and e['state'] == 'stopped', 'got %r' % (e and e['raw']))
    if reason:
        r.ok('reason-wording', e is not None and e['reason'] is not None and reason in e['reason'],
             'got %r, want a reason containing %r' % (e and e['raw'], reason))
    else:
        r.ok('reason-given', e is not None and bool(e['reason']), 'got %r' % (e and e['raw']))
    if within is not None:
        base = since if since is not None else r.notes_with('start ')[0].ts
        last = [n.ts for n in r.notes if n.text == 'alive']
        dt = (max(last) - base) if last else None
        r.ok('process-ended-within-%gs' % within, dt is not None and dt <= within,
             'the process was last alive %s s after the offending write (want at most %g)' % (None if dt is None else round(dt, 2), within))
    if doc is not None:
        r.eq('rhun-document-intact', r.doc('d'), doc)
    e2 = r.ext('e2', 'conf/full')
    r.ok('command-does-not-restart-it', e2 is not None and e2['state'] == 'stopped' and r.hello_count() == 1,
         'after a further command: %r, processes started: %d' % (e2 and e2['raw'], r.hello_count()))


def limit_scn(name, ops, reason=None, within=3.0, wait=2500, doc=A_TXT, env=None, since_note=None, slow=False):
    def chk(r):
        since = None
        if since_note:
            ns = r.notes_with(since_note)
            since = ns[0].ts if ns else None
        limit_check(r, reason=reason, within=within, since=since, doc=doc)
    e = dict(HB)
    e.update(env or {})
    plain(name, ['conf/full'], ops=ops, env=e, slow=slow,
          script=[OPEN_A, 'cmd full.go', 'wait %d' % wait] + pe('e') + pd('d') + ['cmd full.go', 'wait 700'] + pe('e2') + ['quit'],
          check=chk)


limit_scn('life.three-syntax-errors-stop-it',
          [["raw", "garbage one\n"], ["raw", "garbage two\n"], ["raw", "garbage three\n"], ["mark", "sent"], ["sleep", 5]],
          since_note='mark sent')
limit_scn('life.three-bad-requests-stop-it',
          [["send", [["req", "range", "1 1"], ["req", "range", "1 1"], ["req", "range", "1 1"]]], ["mark", "sent"], ["sleep", 5]],
          since_note='send-write')
limit_scn('life.line-over-64KB-stops-it', [["mark", "sent"], ["raw", {"rep": "a", "n": 70000}], ["sleep", 5]],
          reason='a line over 64 KB without an end', since_note='mark sent')
limit_scn('life.replace-body-over-cap-stops-it-at-once',
          [["mark", "sent"], ["req", "replace", "{DOC:a.txt} 0 1 1 1 1 5242880"], ["sleep", 5]],
          reason='a body over its cap', since_note='mark sent')
limit_scn('life.insert-body-over-cap-stops-it-at-once',
          [["mark", "sent"], ["req", "insert", "{DOC:a.txt} 0 5242880"], ["sleep", 5]],
          reason='a body over its cap', since_note='mark sent')
limit_scn('life.body-over-cap-fully-sent',
          [["mark", "sent"], ["replace", "a.txt", "cur", 1, 1, 1, 1, {"rep": "z" * 1023 + "\n", "n": 5120}], ["sleep", 5]],
          reason='a body over its cap', since_note='mark sent')
limit_scn('life.flooding-stops-it', [["mark", "sent"], ["flood", 60000, "cancel 1"], ["sleep", 5]],
          reason='flooding', within=4.0, wait=5500, since_note='mark sent')

# ================================================================ sections 3, 4, 7: fragmentation, coalescing, big bodies

FRAG_STEPS = [
    ('text', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('replace', ["replace", "a.txt", "cur", 1, 1, 1, 6, "ALPHA"], ('okv',)),
    ('text-after', ["text", "a.txt"], ('body', b'ALPHA\nbeta\ngamma\n')),
    ('toast', ["toast", "slowly"], ('ok',)),
    ('status', ["status", "one byte at a time"], ('ok',)),
    ('select', ["select", "a.txt", 2, 1, 2, 3], ('ok',)),
    ('unknown', ["req", "frobnicate", "x"], ('err', 'unknown')),
    DONE,
]


def chk_frag(r):
    r.finished()
    check_steps(r, FRAG_STEPS)
    e = r.ext('s', 'conf/full')
    r.ok('hello-and-requests-written-a-byte-at-a-time-work', e is not None and e['state'] == 'running' and e['status'] == 'one byte at a time',
         'got %r' % (e and e['raw']))
    r.eq('document-text', r.doc('d'), 'ALPHA\nbeta\ngamma\n')


plain('tr.one-byte-writes', ['conf/full'], ops=split_steps(FRAG_STEPS)[0], env={'CONF_FRAG': '1'},
      script=std(pre=[OPEN_A], wait=2500, post=pe('s') + pd('d')), check=chk_frag)

BODY20 = '0123456789ABCDEFGHIJ'


def split_scn(name, split, text_after, extra_check=None):
    steps = [('replace', ["replace", "a.txt", "cur", 1, 1, 1, 6, BODY20, {"split": split}], ('okv',)),
             ('text-after', ["text", "a.txt"], ('body', text_after.encode())), DONE]

    def chk(r):
        r.finished()
        check_steps(r, steps)
        part = r.notes_with('sent-part')
        rest = r.notes_with('sent-rest')
        rep = r.reply(1)
        r.ok('no-reply-before-the-rest-arrives', bool(rest) and rep is not None and rep.ts >= rest[0].ts,
             'reply at %s, rest sent at %s' % (rep and round(rep.ts, 3), rest and round(rest[0].ts, 3)))
        r.eq('document-text', r.doc('d'), text_after)
    plain('tr.' + name, ['conf/full'], ops=split_steps(steps)[0],
          script=std(pre=[OPEN_A], wait=2200, post=pd('d')), check=chk)


split_scn('body-split-in-the-middle-with-a-pause', [10, 0.8, "body"], BODY20 + '\nbeta\ngamma\n')
split_scn('header-split-in-the-middle-with-a-pause', [6, 0.6], BODY20 + '\nbeta\ngamma\n')

SHORT_STEPS = [
    ('replace-with-LEN-3', ["replace", "a.txt", "cur", 1, 1, 1, 1, "abcXY\n", {"len": 3}], ('okv',)),
    ('after-the-leftover', ["toast", "still alive"], ('ok',)),
    ('text', ["text", "a.txt"], ('body', b'abcalpha\nbeta\ngamma\n')),
    DONE,
]


def chk_short(r):
    r.finished()
    check_steps(r, SHORT_STEPS)
    e = r.ext('e', 'conf/full')
    r.ok('running', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))
    r.eq('document-text', r.doc('d'), 'abcalpha\nbeta\ngamma\n')


plain('tr.body-shorter-than-its-bytes-reads-exactly-LEN', ['conf/full'], ops=split_steps(SHORT_STEPS)[0],
      script=std(pre=[OPEN_A], wait=1500, post=pe('e') + pd('d')), check=chk_short)

BATCH_OPS = [
    ["batch", [
        ["toast", "one"],
        ["status", "two"],
        ["text", "a.txt"],
        ["range", "a.txt", 1, 1, 2, 1],
        ["replace", "a.txt", "cur", 1, 1, 1, 6, "ALPHA"],
        ["replace", "a.txt", "cur", 1, 1, 1, 6, "9 toast 2\nxx\n"],
        ["req", "frobnicate", "x"],
        ["replace", "a.txt", "cur", 1, 1, 1, 1, "5 status 3\nabc\n"],
        ["toast", "last"],
    ]],
    ["text", "a.txt"],
    DONE,
]
BATCH_CASES = [
    ('toast', ('ok',)), ('status', ('ok',)), ('text', ('body', A_TXT.encode())),
    ('range', ('body', b'alpha\n')), ('replace-first', ('okv',)),
    ('replace-second-sees-the-first-and-is-stale', ('stale',)),
    ('unknown-verb', ('err', 'unknown')), ('replace-third-stale-too', ('stale',)),
    ('toast-last', ('ok',)), ('text-after', ('body', b'ALPHA\nbeta\ngamma\n')),
]


def chk_batch(r):
    r.finished()
    got = check_cases(r, BATCH_CASES)
    first = got['replace-first']
    cur = int(first.f[2]) if first and first.f[0] == 'ok' else None
    for k, rid in (('replace-second-sees-the-first-and-is-stale', 6), ('replace-third-stale-too', 8)):
        m = got.get(k)
        r.eq(k + '-text-is-the-version-the-first-produced', m and m.line, 'error %d stale %s' % (rid, cur))
    r.eq('bodies-of-stale-requests-were-skipped-not-run', r.ext('e', 'conf/full') and r.ext('e', 'conf/full')['status'], 'two')
    r.eq('document-text', r.doc('d'), 'ALPHA\nbeta\ngamma\n')
    e = r.ext('e', 'conf/full')
    r.ok('running', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))


plain('tr.many-messages-in-one-write', ['conf/full'], ops=BATCH_OPS,
      script=std(pre=[OPEN_A], wait=1500, post=pe('e') + pd('d')), check=chk_batch)


def chk_burst(r):
    r.finished()
    oks = [m.line for m in r.replies()]
    want = ['ok %d' % i for i in range(1, 601)]
    r.eq('600-requests-in-one-write-are-all-answered', sorted(oks), sorted(want))
    e = r.ext('e', 'conf/full')
    r.ok('a-burst-is-not-flooding', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))


plain('tr.burst-of-600-requests', ['conf/full'], ops=[["batch", [["cancel", 1]] * 600], DONE],
      script=std(wait=1500, post=pe('e')), check=chk_burst)

BIG = ('x' * 127 + '\n') * 24576          # exactly 3 MiB
BIG_STEPS = [
    ('replace-3MB', ["replace", "a.txt", "cur", 1, 1, 4, 1, {"rep": "x" * 127 + "\n", "n": 24576}], ('okv',)),
    ('text-3MB', ["text", "a.txt"], ('body', BIG.encode())),
    ('range-3MB', ["range", "a.txt", 1, 1, 24577, 1], ('body', BIG.encode())),
    ('status-after', ["status", "after the big ones"], ('ok',)),
    DONE,
]


def chk_big(r):
    r.finished()
    check_steps(r, BIG_STEPS)
    e = r.ext('e', 'conf/full')
    r.ok('running', e is not None and e['state'] == 'running' and e['status'] == 'after the big ones', 'got %r' % (e and e['raw']))


plain('tr.3MB-bodies-both-ways', ['conf/full'], ops=split_steps(BIG_STEPS)[0],
      script=std(pre=[OPEN_A], wait=3500, post=pe('e')), check=chk_big, timeout=90)

LONG_STEPS = [
    ('line-of-60000-bytes', ["req", "frobnicate", "a" * 60000], ('err', 'unknown')),
    ('after', ["text", "a.txt"], ('body', A_TXT.encode())),
    DONE,
]


def chk_long(r):
    r.finished()
    check_steps(r, LONG_STEPS)
    e = r.ext('e', 'conf/full')
    r.ok('running', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))


plain('tr.long-line-under-the-cap', ['conf/full'], ops=split_steps(LONG_STEPS)[0],
      script=std(pre=[OPEN_A], wait=1200, post=pe('e')), check=chk_long)

RESET_OPS = [["raw", "garbage one\n"], ["raw", "garbage two\n"], ["toast", "a"], ["raw", "garbage three\n"],
             ["raw", "garbage four\n"], ["toast", "b"], ["raw", "garbage five\n"], ["raw", "garbage six\n"],
             ["toast", "c"], ["sleep", 0.3], DONE]


def chk_reset(r):
    r.finished()
    r.eq('well-formed-requests-answered', sorted(m.line for m in r.replies() if m.f[1] != '0'), ['ok 1', 'ok 2', 'ok 3'])
    e = r.ext('e', 'conf/full')
    r.ok('two-bad-lines-then-a-good-request-keep-it-running', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))


plain('tr.syntax-count-resets-on-a-good-request', ['conf/full'], ops=RESET_OPS, script=std(wait=1500, post=pe('e')), check=chk_reset)


def chk_eof_body(r):
    e = r.ext('e', 'conf/full')
    r.ok('exit-in-the-middle-of-a-body-is-a-crash', e is not None and e['state'] == 'crashed', 'got %r' % (e and e['raw']))
    r.eq('rhun-document-intact', r.doc('d'), A_TXT)
    r.eq('dirty-flag-clean', r.state('s').get('dirty'), '0')


plain('tr.exit-in-the-middle-of-a-body', ['conf/full'],
      ops=[["replace", "a.txt", "cur", 1, 1, 4, 1, "0123456789", {"len": 100, "nowait": True}], ["sleep", 0.3], ["exit", 0]],
      script=std(pre=[OPEN_A], wait=1000, post=pe('e') + pd('d') + ps('s')), check=chk_eof_body)

# ---- the outgoing queue


def notread_scn(name, n_text, reason, lo, hi, slow=False, wait=7000, alive_at=None):
    ops = [["replace", "a.txt", "cur", 1, 1, 4, 1, {"rep": "x" * 127 + "\n", "n": 24576}],
           ["mark", "sent"], ["send", [["text", "a.txt"]] * n_text], ["noread", 60]]

    def chk(r):
        e = r.ext('e', 'conf/full')
        r.ok('stopped', e is not None and e['state'] == 'stopped', 'got %r' % (e and e['raw']))
        r.ok('reason-wording', e is not None and e['reason'] is not None and reason in e['reason'],
             'got %r, want a reason containing %r' % (e and e['raw'], reason))
        sent = [n.ts for n in r.notes if n.text == 'send-write 4' or n.text == 'send-write %d' % n_text]
        alive = [n.ts for n in r.notes if n.text == 'alive']
        dt = (max(alive) - sent[0]) if sent and alive else None
        r.ok('stopped-%g-to-%g-s-after-the-queue-filled' % (lo, hi), dt is not None and lo <= dt <= hi,
             'the process was last alive %s s after the requests (want %g to %g)' % (None if dt is None else round(dt, 2), lo, hi))
    plain(name, ['conf/full'], ops=ops, env=HB, slow=slow, timeout=90,
          script=[OPEN_A, 'cmd full.go', 'wait %d' % wait] + pe('e') + ['quit'], check=chk)


notread_scn('tr.not-reading-8MB-queue-stops-it-at-once', 4, 'not reading', -0.3, 4.0, wait=7000)
notread_scn('tr.not-reading-full-queue-stops-it-after-10-seconds', 2, 'not reading', 8.5, 13.0, slow=True, wait=16000)

# ================================================================ closing the pipes and stopping


def chk_close(r):
    e = r.ext('e', 'conf/full')
    r.ok('stopped-when-the-program-closes-its-pipe', e is not None and e['state'] != 'running', 'got %r' % (e and e['raw']))
    closed = r.notes_with('closing ')
    alive = [n.ts for n in r.notes if n.text == 'alive' and closed and n.ts >= closed[0].ts]
    # no heartbeat after the close is the best outcome: rhun ended it before the next beat
    dt = (max(alive) - closed[0].ts) if alive else (0.0 if closed else None)
    r.ok('process-ended-within-3s', dt is not None and dt <= 3.0,
         'the process was last alive %s s after it closed its pipe' % (None if dt is None else round(dt, 2)))


plain('life.closing-its-output-stops-it', ['conf/full'], ops=[["close", "stdout"], ["sleep", 30]], env=HB,
      script=['cmd full.go', 'wait 4500'] + pe('e') + ['quit'], check=chk_close)
plain('life.closing-its-input-stops-it', ['conf/full'], ops=[["close", "stdin"], ["sleep", 30]], env=HB,
      script=['cmd full.go', 'wait 1000', 'cmd full.go2', 'wait 3500'] + pe('e') + ['quit'], check=chk_close)


def chk_quit_polite(r):
    r.ok('bye-sent-when-rhun-quits', '0 bye' in r.lines(), 'received: %r' % r.lines()[-3:])
    r.eq('bye-is-the-last-line', r.lines()[-1:], ['0 bye'])
    r.eq('left-on-bye', [n.text for n in r.notes if n.text.startswith('exit')], ['exit bye code=0'])


plain('life.quit-sends-bye', ['conf/full'], ops=[DONE], script=std(wait=800), check=chk_quit_polite)


def chk_quit_stubborn(r):
    byes = [m for m in r.msgs if m.line == '0 bye']
    term = [n for n in r.notes if n.text == 'sigterm']
    r.ok('bye-sent', len(byes) == 1, 'bye lines: %d' % len(byes))
    r.ok('SIGTERM-follows-bye-after-about-300ms', bool(byes) and bool(term) and 0.2 <= term[0].ts - byes[0].ts <= 1.0,
         'SIGTERM %s s after bye (rhun quitting waits up to 300 ms)' % (round(term[0].ts - byes[0].ts, 3) if byes and term else None))
    kids = [int(n.text.split()[1]) for n in r.notes_with('child ')]
    r.ok('SIGTERM-reaches-the-programs-it-started', kids and not any(alive(k) for k in kids),
         'child processes still alive: %s' % [k for k in kids if alive(k)])
    r.ok('rhun-did-not-hang', r.elapsed < 8.0, 'rhun took %.1f s' % r.elapsed)


plain('life.quit-stops-a-program-that-ignores-bye', ['conf/full'], ops=[["ignore", "bye"], ["spawn"], DONE], env=HB,
      script=std(wait=800), check=chk_quit_stubborn)

# ================================================================ section 4: concurrent activation (protocol 1.1)


def chk_slots(r):
    a, b = r.exts('a'), r.exts('b')
    slots_a = {k: v for k, v in a.items() if k.startswith('conf/slot')}
    run_a = sorted(k for k, v in slots_a.items() if v['state'] == 'running')
    wait_a = sorted(k for k, v in slots_a.items() if v['state'] != 'running')
    r.eq('17-extensions-listed', len(slots_a), 17)
    r.eq('17-run-at-once', len(run_a), 17)
    r.eq('no-extension-waits-for-an-idle-slot', len(wait_a), 0)
    r.ok('all-running-extensions-have-processes', all(v['pid'] not in (None, '0') for v in slots_a.values()),
         'got %r' % [v['raw'] for v in slots_a.values()])
    slots_b = {k: v for k, v in b.items() if k.startswith('conf/slot')}
    run_b = sorted(k for k, v in slots_b.items() if v['state'] == 'running')
    r.ok('one-exit-leaves-the-other-sixteen-running', len(run_b) == 16 and slots_b.get('conf/slot01', {}).get('state') == 'crashed' and
         all(v['state'] == 'running' for k, v in slots_b.items() if k != 'conf/slot01'),
         'running %d, slot01 %r, states %r' % (len(run_b), slots_b.get('conf/slot01', {}).get('state'),
                                                  sorted(set(v['state'] for v in slots_b.values()))))
    r.eq('every-extension-started-once', r.starts(), 17)


SLOTS = ['conf/slot%02d' % i for i in range(1, 18)]
plain('life.seventeen-at-once', SLOTS, env={'CONF_SCENARIO': 'slot', 'CONF_SLOT_EXIT': 'slot01'}, timeout=60,
      script=['wait 1300'] + pe('a') + ['wait 4000'] + pe('b') + ['quit'], check=chk_slots)

# ================================================================ section 2: [subscribe]

SUB_SCRIPT_TAIL = ['type x', 'wait 300', 'key shift+Right', 'wait 300', 'cmd save', 'wait 300',
                   'open {P}/a.txt', 'wait 200', 'type y', 'wait 300', 'key shift+Left', 'wait 300', 'cmd save', 'wait 400', 'quit']


def chk_gosub(r):
    names = sorted(r.opened_names())
    r.eq('opened-only-for-matching-names', names, ['Makefile', 'main.go'])
    known = {str(r.docid('main.go')), str(r.docid('Makefile'))}
    cur = [m for m in r.events('cursor')]
    r.ok('cursor-only-for-documents-opened-named', bool(cur) and all(m.f[2] in known for m in cur),
         'cursor events %r, announced documents %r' % ([m.line for m in cur], sorted(known)))
    ch = [m for m in r.events('changed')]
    r.ok('changed-only-for-documents-opened-named', bool(ch) and all(m.f[2] in known for m in ch),
         'changed events %r, announced documents %r' % ([m.line for m in ch], sorted(known)))
    r.eq('selection-no-means-none', [m.line for m in r.events('selection')], [])
    r.eq('saved-no-means-none', [m.line for m in r.events('saved')], [])


plain('sub.opened-names-gate-everything', ['conf/gosub'], ops=[DONE],
      script=['open {P}/a.txt', 'open {P}/data.json', 'open {P}/Makefile', OPEN_GO, 'cmd gosub.go', 'wait-ext gosub running', 'wait 600'] + SUB_SCRIPT_TAIL,
      check=chk_gosub)


def chk_nosub(r):
    names = sorted(set(m.f[1] for m in r.msgs if m.f[0] == '0'))
    r.ok('no-subscription-no-events-but-ready-active-run-bye', set(names) <= {'ready', 'active', 'run', 'bye'} and 'ready' in names,
         'event names seen: %r' % names)


plain('sub.default-is-no', ['conf/nosub'], ops=[DONE],
      script=['open {P}/a.txt', 'open {P}/data.json', 'open {P}/Makefile', OPEN_GO, 'cmd nosub.go', 'wait-ext nosub running', 'wait 600'] + SUB_SCRIPT_TAIL,
      check=chk_nosub)

PAT_SCRIPT = ['open {P}/a.txt', 'open {P}/data.json', 'open {P}/Makefile', 'cmd pat.go', 'wait-ext pat running', 'wait 600',
              'type X', 'wait 300', 'cmd save', 'wait 300',
              'open {P}/data.json', 'wait 200', 'type Y', 'wait 300', 'cmd save', 'wait 300',
              'open {P}/a.txt', 'wait 200', 'type Z', 'wait 300', 'cmd save', 'wait 400', 'quit']


def chk_pat(r):
    mk, js, a = r.docid('Makefile'), r.docid('data.json'), r.docid('a.txt')
    r.eq('opened-for-all', sorted(r.opened_names()), ['Makefile', 'a.txt', 'data.json'])
    ch = r.events('changed')
    r.eq('changed-for-Makefile-and-*.json-only', sorted(set(m.f[2] for m in ch)), sorted([str(mk), str(js)]))
    sv = r.events('saved')
    r.eq('saved-for-*.json-only', [m.f[2] for m in sv], [str(js)])
    jch = [int(m.f[3]) for m in ch if m.f[2] == str(js)]
    r.ok('saved-carries-the-current-version', len(sv) == 1 and jch and int(sv[0].f[3]) == jch[-1],
         'saved %r, last changed version of data.json %r' % ([m.line for m in sv], jch[-1:]))
    r.eq('cursor-no', [m.line for m in r.events('cursor')], [])
    r.eq('selection-no', [m.line for m in r.events('selection')], [])
    with open(os.path.join(r.dirs['proj'], 'data.json')) as f:
        r.ok('the-save-wrote-the-file', f.read().startswith('Y'), 'data.json starts with %r' % open(os.path.join(r.dirs['proj'], 'data.json')).read()[:5])


plain('sub.name-patterns', ['conf/pat'], ops=[DONE], script=PAT_SCRIPT, check=chk_pat)


def chk_closed(r):
    gid = r.docid('main.go')
    cl = [m.line for m in r.events('closed')]
    r.eq('closed-event', cl, ['0 closed %d' % gid])
    again = [m for m in r.events('opened') if m.line.endswith('/main.go')]
    r.ok('reopened-file-gets-a-new-id', len(again) == 2 and again[0].f[2] != again[1].f[2],
         'opened events for main.go: %r' % [m.line for m in again])


plain('ev.closed-and-reopened', ['conf/full'], ops=[DONE],
      script=[OPEN_A, OPEN_GO, 'cmd full.go', 'wait-ext full running', 'wait 700', 'cmd close_tab', 'wait 400',
              'open {P}/main.go', 'wait 400', 'quit'], check=chk_closed)


def chk_user_change(r):
    r.finished()
    doc = r.docid('a.txt')
    ch = [int(m.f[3]) for m in r.events('changed') if m.f[2] == str(doc)]
    t = r.reply(1)
    r.ok('changed-after-typing', bool(ch) and ch == sorted(ch) and ch[0] > int(r.events('opened')[0].f[3]), 'changed versions %r' % ch)
    r.ok('text-VERSION-equals-the-last-changed-VERSION', t is not None and ch and int(t.f[2]) == ch[-1],
         'text reply %r, changed versions %r' % (t and t.line, ch))
    r.ok('text-has-the-typed-characters', t is not None and t.body is not None and t.body.startswith(b'abc'), 'body %r' % (t and t.body))


plain('ev.changed-by-the-user', ['conf/full'], ops=[["sleep", 1.2], ["text", "a.txt"], DONE],
      script=[OPEN_A, 'cmd full.go', 'wait-ext full running', 'wait 300', 'type abc', 'wait 1700', 'quit'], check=chk_user_change)


def chk_act_make(r):
    b, c = r.ext('b', 'conf/onmake'), r.ext('c', 'conf/onmake')
    r.ok('not-started-for-other-file', b is not None and b['state'] != 'running', 'got %r' % (b and b['raw']))
    r.ok('exact-file-name-starts-it', c is not None and c['state'] == 'running', 'got %r' % (c and c['raw']))


plain('act.open-exact-name', ['conf/onmake'], ops=[DONE],
      script=[OPEN_A, 'wait 600'] + pe('b') + ['open {P}/Makefile', 'wait 1200'] + pe('c') + ['quit'], check=chk_act_make)


def chk_key(r):
    docs = r.docid('a.txt')
    r.eq('default-key-runs-the-command-and-starts-the-extension', [m.line for m in r.events('run')], ['0 run keyed.fmt %d' % docs])


plain('cmd.default-key', ['conf/keyed'], ops=[DONE],
      script=[OPEN_A, 'key ctrl+shift+alt+9', 'wait 1500', 'quit'], check=chk_key)


def chk_reload(r):
    r.finished()
    doc = r.docid('a.txt')
    t1, t2 = r.reply(1), r.reply(2)
    r.ok('text-before', t1 is not None and t1.body == A_TXT.encode(), 'got %r' % (t1 and t1.line))
    r.ok('reloaded-file-keeps-its-id-and-text-follows-the-disk', t2 is not None and t2.f[0] == 'ok' and t2.sha == sha(b'fresh from disk\n'),
         'second text reply %r' % (t2 and t2.line))
    r.ok('reloaded-file-gets-a-new-version', t1 is not None and t2 is not None and len(t2.f) > 2 and int(t2.f[2]) > int(t1.f[2]),
         'versions %r -> %r' % (t1 and t1.f[2:3], t2 and t2.f[2:3]))
    ch = [m.line for m in r.events('changed') if m.f[2] == str(doc)]
    r.ok('changed-event-for-the-reload', bool(ch), 'no changed event for document %s' % doc)
    r.eq('no-second-opened-event', len(r.events('opened')), 1)


plain('ev.reload-keeps-the-id', ['conf/fullw'],
      ops=[["text", "a.txt"], ["writefile", "{PROJECT}/a.txt", "fresh from disk\n"], ["sleep", 3.0], ["text", "a.txt"], DONE],
      script=std(pre=[OPEN_A], wait=5200, cmd=('cmd fullw.go', 'wait-ext fullw running')), check=chk_reload)

# ================================================================ gate 4: register, option, prompts, term, cmd
# The scripted extension is started by `cmd full.go`; its ops begin with `waitrun`, which takes that
# first `run`.  A second `waitrun` makes it wait for `cmd full.go2`, so the script decides when the
# next request is sent instead of guessing with sleeps.  A body's "LEN" is written `{LEN}` in `req`.

W2 = ['wait 300', 'wait 300']
TRIG1 = ['open {P}/a.txt', 'cmd full.go', 'wait-ext full running'] + W2
TRIG2 = ['open {P}/a.txt', 'cmd full.go', 'wait-ext full running', 'wait 300', 'cmd full.go2'] + W2
TRIGT2 = ['open {P}/a.txt', 'cmd term.go', 'wait-ext term running', 'wait 300', 'cmd term.go2'] + W2
WR = ["waitrun"]


def preq(verb, *lines):
    """A prompt request whose body is the lines joined by LF (no trailing LF)."""
    return ["req", verb, "{LEN}", '\n'.join(lines)]


def pal(label):
    return ['echo @' + label, 'print-palette']


def popt(label):
    return ['echo @' + label, 'print-options']


def pterm(label):
    return ['echo @' + label, 'print-term']


def palette(r, label):
    """(first line, field text, rows, selected row) of a print-palette segment; the first line is
    `none` when no prompt is open.  Rows are the text after the two-character marker."""
    seg = [ln for ln in r.seg(label) if ln != '' and not ln.startswith('ext=')]
    if not seg:
        return (None, None, [], None)
    if seg[0] == 'none':
        return ('none', None, [], None)
    field, rows, sel = None, [], None
    for ln in seg[1:]:
        if ln.startswith('field='):
            field = ln[6:]
        elif ln.startswith('> '):
            sel = ln[2:]
            rows.append(ln[2:])
        elif ln.startswith('  '):
            rows.append(ln[2:])
    return (seg[0], field, rows, sel)


def opts_of(r, label):
    """print-options as {key: value}; the booleans as bool."""
    for ln in r.seg(label):
        if ln.startswith('tab_width='):
            d = dict(kv.split('=', 1) for kv in ln.split(' ') if '=' in kv)
            return {k: (int(v) if k == 'tab_width' else v in ('1', 'true', 'yes')) for k, v in d.items()}
    return None


def is_err(m, rid, code):
    return m is not None and re.match(r'^error %d %s( .*)?$' % (rid, re.escape(code)), m.line) is not None


def ln_of(m):
    return m.line if m is not None else None


# ---------------------------------------------------------------- register (section 8, 3, 4.5)

def chk_register_runs(r):
    r.finished()
    r.eq('register-name-title', ln_of(r.reply(1)), 'ok 1')
    r.eq('register-with-a-key', ln_of(r.reply(2)), 'ok 2')
    e = r.ext('e', 'conf/full')
    cmds = set((e['cmds'] or '').split(',')) if e else set()
    r.ok('listed-with-the-manifest-commands', {'full.go', 'full.go2', 'full.hello', 'full.keyed'} <= cmds, 'got %r' % (e and e['raw']))
    doc = r.docid('a.txt')
    runs = [m.line for m in r.events('run')]
    r.eq('cmd-and-the-palette-both-give-the-run-event', runs.count('0 run full.hello %d' % doc), 2)
    r.eq('default-key-gives-the-run-event', runs.count('0 run full.keyed %d' % doc), 1)
    head, field, rows, sel = palette(r, 'p')
    r.ok('palette-shows-the-title-with-the-extension', any(re.match(r'^Say Hello \(.*full.*\)', x, re.I) for x in rows),
         'palette rows after typing "say hello": %r' % rows)


plain('register.runs', ['conf/full'],
      ops=[WR, ["req", "register", "hello - Say Hello"], ["req", "register", "keyed ctrl+shift+alt+8 Keyed Thing"], DONE],
      script=TRIG1 + pe('e') + ['cmd full.hello', 'wait 300', 'wait 300', 'key ctrl+shift+alt+8', 'wait 300', 'wait 300',
                                'key ctrl+shift+p', 'wait 300', 'type say hello', 'wait 300'] + ['echo @p', 'print-palette', 'key Return', 'wait 300', 'wait 300', 'quit'],
      check=chk_register_runs)

REG_STEPS = [
    ('register', ["req", "register", "hello - Say Hello"], ('ok',)),
    ('name-in-use-by-an-earlier-register-is-refused', ["req", "register", "hello - Again"], ('err', 'refused')),
    ('name-in-use-by-a-manifest-command-is-refused', ["req", "register", "go - Mine"], ('err', 'refused')),
    ('after-refused-1', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('upper-case-name-is-syntax', ["req", "register", "Bad - Title"], ('err', 'syntax')),
    ('after-syntax-1', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('dash-in-name-is-syntax', ["req", "register", "a-b - Title"], ('err', 'syntax')),
    ('after-syntax-2', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('name-of-only-a-dash-is-syntax', ["req", "register", "- - Title"], ('err', 'syntax')),
    ('after-syntax-3', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('missing-title-is-syntax', ["req", "register", "onlyname -"], ('err', 'syntax')),
    ('after-syntax-4', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('no-arguments-is-syntax', ["req", "register"], ('err', 'syntax')),
    ('after-syntax-5', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('name-of-41-bytes-is-syntax', ["req", "register", "a" * 41 + " - Title"], ('err', 'syntax')),
    ('after-syntax-6', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('name-of-40-bytes-is-ok', ["req", "register", "b" * 40 + " - Title"], ('ok',)),
    ('title-of-100-bytes-is-cut-not-an-error', ["req", "register", "longtitle - " + "T" * 100], ('ok',)),
    ('title-of-100-bytes-in-2-byte-characters-is-cut-not-an-error', ["req", "register", "mbtitle - " + "é" * 50], ('ok',)),
    ('digits-and-underscore-are-fine', ["req", "register", "a_1_9 - Title"], ('ok',)),
    DONE,
]
REG_OPS, REG_CASES = split_steps(REG_STEPS)


def chk_register_refused(r):
    r.finished()
    check_steps(r, REG_STEPS)
    e = r.ext('e', 'conf/full')
    cmds = set((e['cmds'] or '').split(',')) if e else set()
    want = {'full.go', 'full.go2', 'full.hello', 'full.' + 'b' * 40, 'full.longtitle', 'full.mbtitle', 'full.a_1_9'}
    r.eq('only-the-accepted-names-are-commands', cmds, want)
    r.ok('extension-still-running', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))


plain('register.refused-or-malformed', ['conf/full'], ops=REG_OPS,
      script=std(pre=[OPEN_A], wait=1500, post=pe('e')), check=chk_register_refused)


def chk_register_bound_key(r):
    r.finished()
    r.eq('register-with-a-bound-key-is-still-ok', ln_of(r.reply(1)), 'ok 1')
    r.eq('the-built-in-key-did-not-run-the-extension-command', [m.line for m in r.events('run') if 'fake' in m.line], [])
    with open(os.path.join(r.dirs['proj'], 'a.txt'), 'rb') as f:
        r.eq('the-built-in-command-kept-the-key-it-saved', f.read(), A_TXT.encode() + b'X\n')
    r.eq('the-command-itself-exists', 'full.fake' in ((r.ext('e', 'conf/full') or {}).get('cmds') or '').split(','), True)


plain('register.keys-already-bound-stay-with-their-command', ['conf/full'],
      ops=[WR, ["req", "register", "fake ctrl+s Fake Save"], DONE],
      script=TRIG1 + ['key ctrl+End', 'type X', 'key ctrl+s', 'wait 400', 'wait 400'] + pe('e') + ['quit'], check=chk_register_bound_key)

def chk_register_vanish(r):
    b, a, c = r.ext('b', 'conf/full'), r.ext('a', 'conf/full'), r.ext('c', 'conf/full')
    r.ok('registered-while-running', b is not None and b['state'] == 'running' and 'full.hello' in (b['cmds'] or '').split(','), 'got %r' % (b and b['raw']))
    r.ok('exit-is-a-crash', a is not None and a['state'] == 'crashed', 'got %r' % (a and a['raw']))
    r.eq('manifest-commands-stay-the-registered-one-goes', sorted((a['cmds'] or '').split(',')) if a else None, ['full.go', 'full.go2'])
    r.eq('invoking-the-removed-command-starts-nothing', r.hello_count(), 1)
    r.eq('invoking-the-removed-command-sends-no-run', [m.line.split(' ')[2] for m in r.events('run')], ['full.go', 'full.go2'])
    r.ok('still-crashed', c is not None and c['state'] == 'crashed', 'got %r' % (c and c['raw']))


plain('register.vanish-when-the-extension-exits', ['conf/full'],
      ops=[WR, ["req", "register", "hello - Say Hello"], WR, ["exit", 0]],
      script=TRIG1 + pe('b') + ['cmd full.go2', 'wait-ext full crashed'] + pe('a') + ['cmd full.hello', 'wait 600'] + pe('c') + ['quit'],
      check=chk_register_vanish)


def chk_register_vanish_stop(r):
    b, a = r.ext('b', 'conf/full'), r.ext('a', 'conf/full')
    r.ok('registered-while-running', b is not None and b['state'] == 'running' and 'full.hello' in (b['cmds'] or '').split(','), 'got %r' % (b and b['raw']))
    r.ok('three-syntax-errors-stop-it', a is not None and a['state'] == 'stopped', 'got %r' % (a and a['raw']))
    r.eq('registered-command-is-removed-when-it-is-stopped', sorted((a['cmds'] or '').split(',')) if a else None, ['full.go', 'full.go2'])


plain('register.vanish-when-the-extension-is-stopped', ['conf/full'],
      ops=[WR, ["req", "register", "hello - Say Hello"], ["sleep", 1.0], ["raw", "garbage one\n"], ["raw", "garbage two\n"],
           ["raw", "garbage three\n"], ["sleep", 5]],
      script=TRIG1 + pe('b') + ['wait 2500'] + pe('a') + ['quit'], check=chk_register_vanish_stop)

# ---------------------------------------------------------------- option (section 6c)

OPT_SET = [WR,
           ["req", "option", "{DOC:a.txt} tab_width 7"],
           ["req", "option", "{DOC:a.txt} insert_spaces no"],
           ["req", "option", "{DOC:a.txt} trim_trailing_whitespace yes"],
           ["req", "option", "{DOC:a.txt} final_newline no"]]
OPT_A = {'tab_width': 7, 'insert_spaces': False, 'trim_trailing_whitespace': True, 'final_newline': False}


def config_has_no(r, text):
    p = os.path.join(r.dirs['cfg'], 'rhun', 'config')
    try:
        with open(p) as f:
            data = f.read()
    except OSError:
        return True, ''
    bad = [ln for ln in data.splitlines() if 'tab_width' in ln and text in ln]
    return not bad, 'config line %r' % bad


def chk_opt_one_document(r):
    r.finished()
    for i in (1, 2, 3, 4):
        r.eq('option-%d-ok' % i, ln_of(r.reply(i)), 'ok %d' % i)
    base = opts_of(r, 'before')
    a, b, a2 = opts_of(r, 'a'), opts_of(r, 'b'), opts_of(r, 'a2')
    r.ok('baseline-printed', base is not None, 'no print-options line in %r' % r.seg('before'))
    r.eq('the-document-has-the-four-overrides', a, OPT_A)
    r.eq('the-other-document-keeps-the-users-settings', b, base)
    r.eq('the-overridden-document-keeps-them-when-it-is-active-again', a2, OPT_A)
    r.eq('the-other-document-was-active', r.state('s').get('active'), 'main.go')
    ok, msg = config_has_no(r, '7')
    r.ok('options-never-reach-the-config-file', ok, msg)


plain('option.per-document', ['conf/full'], ops=[["sleep", 0.6]] + OPT_SET + [DONE],
      script=['open {P}/main.go', 'open {P}/a.txt', 'cmd full.go', 'wait-ext full running'] + popt('before') + W2 + W2 + popt('a') +
             ['key ctrl+Tab', 'wait 300'] + ps('s') + popt('b') + ['key ctrl+Tab', 'wait 300'] + popt('a2') + ['quit'],
      check=chk_opt_one_document)


def chk_opt_typing(r):
    r.finished()
    r.ok('Tab-inserts-a-tab-when-insert_spaces-is-no', '\t' in (r.doc('d') or ''), 'document %r' % r.doc('d'))


plain('option.insert_spaces-changes-what-Tab-types', ['conf/full'],
      ops=[["sleep", 0.6], WR, ["req", "option", "{DOC:a.txt} insert_spaces no"], DONE],
      script=TRIG1 + W2 + ['key ctrl+End', 'key Tab'] + pd('d') + ['quit'], check=chk_opt_typing)


def chk_opt_save(r):
    r.finished()
    with open(os.path.join(r.dirs['proj'], 'a.txt'), 'rb') as f:
        data = f.read()
    r.eq('saved-trimmed-and-without-a-final-newline', data, b'alpha\nbeta\ngamma\nx')


plain('option.save-options', ['conf/full'],
      ops=[["sleep", 0.6], WR, ["req", "option", "{DOC:a.txt} trim_trailing_whitespace yes"],
           ["req", "option", "{DOC:a.txt} final_newline no"], DONE],
      script=TRIG1 + W2 + ['key ctrl+End', 'type x', 'key space', 'key space', 'cmd save', 'wait 400', 'wait 400', 'quit'], check=chk_opt_save)


def chk_opt_inactive(r):
    r.finished()
    r.eq('option-ok', ln_of(r.reply(1)), 'ok 1')
    base, a, b = opts_of(r, 'before'), opts_of(r, 'a'), opts_of(r, 'b')
    r.eq('the-active-document-is-untouched', a, base)
    r.eq('the-document-that-was-not-active-has-the-override', b and b['tab_width'], 9)
    r.eq('the-other-document-was-switched-to', r.state('s').get('active'), 'main.go')


plain('option.document-that-is-not-active', ['conf/full'],
      ops=[["sleep", 0.6], WR, ["req", "option", "{DOC:main.go} tab_width 9"], DONE],
      script=['open {P}/main.go', 'open {P}/a.txt', 'cmd full.go', 'wait-ext full running'] + popt('before') + W2 + W2 + popt('a') +
             ['key ctrl+Tab', 'wait 300'] + ps('s') + popt('b') + ['quit'], check=chk_opt_inactive)


def chk_opt_dash(r):
    r.finished()
    base, s, rm = opts_of(r, 'before'), opts_of(r, 'set'), opts_of(r, 'removed')
    r.ok('set', s is not None and s['tab_width'] == 7 and s['insert_spaces'] is False, 'got %r' % s)
    r.eq('dash-removes-the-override-of-that-key', rm and rm['tab_width'], base and base['tab_width'])
    r.eq('the-other-override-stays', rm and rm['insert_spaces'], False)
    r.eq('dash-is-ok', ln_of(r.reply(3)), 'ok 3')


plain('option.dash-removes', ['conf/full'],
      ops=[["sleep", 0.6], WR, ["req", "option", "{DOC:a.txt} tab_width 7"], ["req", "option", "{DOC:a.txt} insert_spaces no"],
           WR, ["req", "option", "{DOC:a.txt} tab_width -"], DONE],
      script=['open {P}/a.txt', 'cmd full.go', 'wait-ext full running'] + popt('before') + W2 + W2 + popt('set') + ['cmd full.go2'] + W2 + popt('removed') + ['quit'],
      check=chk_opt_dash)

OPT_BAD_STEPS = [
    ('unknown-key-is-syntax', ["req", "option", "{DOC:a.txt} tabwidth 4"], ('err', 'syntax')),
    ('ok-1', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('tab_width-0-is-syntax', ["req", "option", "{DOC:a.txt} tab_width 0"], ('err', 'syntax')),
    ('ok-2', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('tab_width-17-is-syntax', ["req", "option", "{DOC:a.txt} tab_width 17"], ('err', 'syntax')),
    ('ok-3', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('tab_width-negative-is-syntax', ["req", "option", "{DOC:a.txt} tab_width -3"], ('err', 'syntax')),
    ('ok-4', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('tab_width-not-a-number-is-syntax', ["req", "option", "{DOC:a.txt} tab_width abc"], ('err', 'syntax')),
    ('ok-5', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('insert_spaces-maybe-is-syntax', ["req", "option", "{DOC:a.txt} insert_spaces maybe"], ('err', 'syntax')),
    ('ok-6', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('final_newline-1-is-syntax-only-yes-and-no-are-values', ["req", "option", "{DOC:a.txt} final_newline 1"], ('err', 'syntax')),
    ('ok-7', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('trim_trailing_whitespace-true-is-syntax', ["req", "option", "{DOC:a.txt} trim_trailing_whitespace true"], ('err', 'syntax')),
    ('ok-8', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('missing-value-is-syntax', ["req", "option", "{DOC:a.txt} tab_width"], ('err', 'syntax')),
    ('ok-9', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('non-numeric-DOC-is-syntax', ["req", "option", "abc tab_width 4"], ('err', 'syntax')),
    ('ok-10', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('no-such-document-is-nodoc', ["req", "option", "424242 tab_width 4"], ('err', 'nodoc')),
    ('tab_width-1-is-ok', ["req", "option", "{DOC:a.txt} tab_width 1"], ('ok',)),
    ('tab_width-16-is-ok', ["req", "option", "{DOC:a.txt} tab_width 16"], ('ok',)),
    DONE,
]
OPT_BAD_OPS, OPT_BAD_CASES = split_steps(OPT_BAD_STEPS)


def chk_opt_bad(r):
    r.finished()
    check_steps(r, OPT_BAD_STEPS)
    base, after = opts_of(r, 'before'), opts_of(r, 'after')
    want = dict(base or {})
    want['tab_width'] = 16
    r.eq('only-the-good-requests-changed-anything', after, want)
    e = r.ext('e', 'conf/full')
    r.ok('extension-still-running', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))


plain('option.bad-keys-and-values', ['conf/full'], ops=[["sleep", 0.6], WR] + OPT_BAD_OPS,
      script=['open {P}/a.txt', 'cmd full.go', 'wait-ext full running'] + popt('before') + ['wait 1500'] + popt('after') + pe('e') + ['quit'],
      check=chk_opt_bad)


def chk_opt_exit(r):
    base, s, after = opts_of(r, 'before'), opts_of(r, 'set'), opts_of(r, 'after')
    r.ok('set-while-running', s is not None and s['tab_width'] == 7 and s['insert_spaces'] is False, 'got %r' % s)
    e = r.ext('e', 'conf/full')
    r.ok('the-extension-exited', e is not None and e['state'] == 'crashed', 'got %r' % (e and e['raw']))
    r.eq('the-users-settings-return', after, base)


plain('option.users-settings-return-when-the-extension-exits', ['conf/full'],
      ops=[["sleep", 0.6], WR, ["req", "option", "{DOC:a.txt} tab_width 7"], ["req", "option", "{DOC:a.txt} insert_spaces no"], WR, ["exit", 0]],
      script=['open {P}/a.txt', 'cmd full.go', 'wait-ext full running'] + popt('before') + W2 + W2 + popt('set') +
             ['cmd full.go2', 'wait-ext full crashed', 'wait 300'] + popt('after') + pe('e') + ['quit'], check=chk_opt_exit)


def chk_opt_stopped(r):
    base, s, after = opts_of(r, 'before'), opts_of(r, 'set'), opts_of(r, 'after')
    r.ok('set-while-running', s is not None and s['tab_width'] == 7, 'got %r' % s)
    e = r.ext('e', 'conf/full')
    r.ok('the-extension-was-stopped', e is not None and e['state'] == 'stopped', 'got %r' % (e and e['raw']))
    r.eq('the-users-settings-return', after, base)


plain('option.users-settings-return-when-the-extension-is-stopped', ['conf/full'],
      ops=[["sleep", 0.6], WR, ["req", "option", "{DOC:a.txt} tab_width 7"], ["sleep", 2.5], ["raw", "garbage one\n"],
           ["raw", "garbage two\n"], ["raw", "garbage three\n"], ["sleep", 5]],
      script=['open {P}/a.txt', 'cmd full.go', 'wait-ext full running'] + popt('before') + W2 + W2 + popt('set') +
             ['wait 3000'] + popt('after') + pe('e') + ['quit'], check=chk_opt_stopped)


def chk_opt_close(r):
    r.finished()
    base, s, again = opts_of(r, 'before'), opts_of(r, 'set'), opts_of(r, 'again')
    r.ok('set-while-open', s is not None and s['tab_width'] == 7, 'got %r' % s)
    r.eq('reopened-document-has-the-users-settings', again, base)


plain('option.removed-when-the-document-closes', ['conf/full'],
      ops=[["sleep", 0.6], WR, ["req", "option", "{DOC:a.txt} tab_width 7"], DONE],
      script=['open {P}/a.txt', 'cmd full.go', 'wait-ext full running'] + popt('before') + W2 + W2 + popt('set') +
             ['cmd close_tab', 'wait 300', 'open {P}/a.txt', 'wait 300'] + popt('again') + ['quit'], check=chk_opt_close)


def chk_opt_two(r):
    base, both, f, t = opts_of(r, 'before'), opts_of(r, 'both'), opts_of(r, 'after-full'), opts_of(r, 'after-term')
    r.eq('the-last-writer-wins', both and both['tab_width'], 6)
    r.eq('one-extension-removing-its-override-leaves-the-others', f and f['tab_width'], 6)
    r.eq('the-last-override-removed-the-users-setting-applies', t and t['tab_width'], base and base['tab_width'])


plain('option.two-extensions-last-writer-wins', ['conf/full', 'conf/term'],
      ops={'full': [["sleep", 0.3], WR, ["req", "option", "{DOC:a.txt} tab_width 5"], WR, ["req", "option", "{DOC:a.txt} tab_width -"], DONE],
           'term': [["sleep", 1.5], WR, ["req", "option", "{DOC:a.txt} tab_width 6"], WR, ["req", "option", "{DOC:a.txt} tab_width -"], DONE]},
      script=['open {P}/a.txt', 'cmd full.go', 'cmd term.go', 'wait-ext full running', 'wait-ext term running'] + popt('before') +
             ['wait 600', 'wait 600', 'wait 600', 'wait 600'] + popt('both') + ['cmd full.go2', 'wait 300', 'wait 300'] + popt('after-full') +
             ['cmd term.go2', 'wait 300', 'wait 300'] + popt('after-term') + ['quit'], check=chk_opt_two)

# ---------------------------------------------------------------- choose

CHOOSE3 = preq('choose', 'Pick one', 'alpha', 'beta', 'gamma')


def chk_choose_filter(r):
    r.finished()
    head, field, rows, sel = palette(r, 'p0')
    r.eq('prompt-is-shown', head, 'choose=Pick one')
    r.eq('items-in-order', rows, ['alpha', 'beta', 'gamma'])
    r.eq('first-item-selected', sel, 'alpha')
    r.eq('filter-starts-empty', field, '')
    head, field, rows, sel = palette(r, 'p1')
    r.eq('typing-filters-the-items', rows, ['gamma'])
    r.eq('field-shows-what-was-typed', field, 'ga')
    r.eq('enter-answers-with-the-number-in-the-original-list', ln_of(r.reply(1)), 'ok 1 3')
    r.eq('prompt-closed-after-the-answer', palette(r, 'p2')[0], 'none')


plain('choose.filter-and-enter', ['conf/full'], ops=[WR, WR, CHOOSE3, DONE],
      script=TRIG2 + pal('p0') + ['type ga', 'wait 300'] + pal('p1') + ['key Return', 'wait 300', 'wait 300'] + pal('p2') + ['quit'],
      check=chk_choose_filter)


def chk_choose_arrows(r):
    r.finished()
    r.eq('enter-on-the-first-item', ln_of(r.reply(1)), 'ok 1 1')
    head, field, rows, sel = palette(r, 'p1')
    r.eq('second-prompt-is-open', head, 'choose=Pick one')
    r.eq('down-moves-the-selection', palette(r, 'p2')[3], 'beta')
    r.eq('enter-on-the-second-item', ln_of(r.reply(2)), 'ok 2 2')


plain('choose.enter-and-arrow-keys', ['conf/full'], ops=[WR, WR, CHOOSE3, CHOOSE3, DONE],
      script=TRIG2 + ['key Return', 'wait 300', 'wait 300'] + pal('p1') + ['key Down', 'wait 300'] + pal('p2') + ['key Return', 'wait 300', 'wait 300', 'quit'],
      check=chk_choose_arrows)


def chk_choose_esc(r):
    r.finished()
    r.eq('prompt-is-shown', palette(r, 'p0')[0], 'choose=Pick one')
    r.ok('escape-answers-cancelled', is_err(r.reply(1), 1, 'cancelled'), 'got %r' % ln_of(r.reply(1)))
    r.eq('prompt-closed', palette(r, 'p1')[0], 'none')
    r.ok('extension-still-running', (r.ext('e', 'conf/full') or {}).get('state') == 'running', 'got %r' % (r.ext('e', 'conf/full') or {}).get('raw'))


plain('choose.escape-cancels', ['conf/full'], ops=[WR, WR, CHOOSE3, DONE],
      script=TRIG2 + pal('p0') + ['key Escape', 'wait 300', 'wait 300'] + pal('p1') + pe('e') + ['quit'], check=chk_choose_esc)


def chk_busy(r):
    r.finished()
    r.eq('first-prompt-stays-open', palette(r, 'p0')[0], 'choose=Pick one')
    r.ok('second-choose-is-busy', is_err(r.reply(2), 2, 'busy'), 'got %r' % ln_of(r.reply(2)))
    r.ok('ask-is-busy', is_err(r.reply(3), 3, 'busy'), 'got %r' % ln_of(r.reply(3)))
    r.ok('first-prompt-is-cancelled-by-escape', is_err(r.reply(1), 1, 'cancelled'), 'got %r' % ln_of(r.reply(1)))
    head = palette(r, 'p1')[0]
    r.eq('after-the-prompt-closes-a-new-one-opens', head, 'choose=Again')
    r.ok('and-is-cancelled-by-escape', is_err(r.reply(4), 4, 'cancelled'), 'got %r' % ln_of(r.reply(4)))
    r.eq('nothing-open-at-the-end', palette(r, 'p2')[0], 'none')


plain('choose.second-prompt-is-busy', ['conf/full'],
      ops=[WR, WR, ["batch", [CHOOSE3, preq('ask', 'Your name?'), preq('choose', 'Other', 'x', 'y')]], preq('choose', 'Again', 'x', 'y'), DONE],
      script=TRIG2 + pal('p0') + ['key Escape', 'wait 300', 'wait 300'] + pal('p1') + ['key Escape', 'wait 300'] + pal('p2') + ['quit'],
      check=chk_busy)


def chk_busy_palette(r):
    r.finished()
    r.ok('choose-while-the-palette-is-open-is-busy', is_err(r.reply(1), 1, 'busy'), 'got %r' % ln_of(r.reply(1)))
    r.ok('the-palette-was-still-the-palette', not (palette(r, 'p0')[0] or '').startswith('choose='), 'print-palette first line %r' % palette(r, 'p0')[0])
    r.eq('after-the-palette-closes-the-prompt-opens', palette(r, 'p1')[0], 'choose=Pick one')


plain('choose.busy-while-the-palette-is-open', ['conf/full'],
      ops=[WR, ["sleep", 1.0], CHOOSE3, WR, CHOOSE3, DONE],
      script=['open {P}/a.txt', 'cmd full.go', 'wait-ext full running', 'key ctrl+shift+p'] + ['wait 300'] * 8 + pal('p0') +
             ['key Escape', 'wait 300', 'cmd full.go2', 'wait 300', 'wait 300'] + pal('p1') + ['key Escape', 'wait 300', 'quit'],
      check=chk_busy_palette)


def chk_cancel_request(r):
    r.finished()
    r.eq('prompt-was-open', palette(r, 'p0')[0], 'choose=Pick one')
    r.eq('cancel-is-ok', ln_of(r.reply(2)), 'ok 2')
    r.ok('the-prompt-request-is-answered-cancelled', is_err(r.reply(1), 1, 'cancelled'), 'got %r' % ln_of(r.reply(1)))
    r.eq('prompt-closed', palette(r, 'p1')[0], 'none')


plain('choose.cancel-request', ['conf/full'],
      ops=[WR, WR, ["send", [CHOOSE3]], ["sleep", 1.5], ["cancel", 1], ["drain", 0.4], DONE],
      script=TRIG2 + pal('p0') + ['wait 600'] * 4 + pal('p1') + ['quit'], check=chk_cancel_request)


def chk_prompt_stop(r):
    r.eq('prompt-was-open', palette(r, 'p0')[0], 'choose=Pick one')
    e = r.ext('e', 'conf/full')
    r.ok('the-extension-exited', e is not None and e['state'] == 'crashed', 'got %r' % (e and e['raw']))
    r.eq('open-prompt-removed-when-the-extension-stops', palette(r, 'p1')[0], 'none')


plain('choose.removed-when-the-extension-stops', ['conf/full'],
      ops=[WR, WR, ["send", [CHOOSE3]], ["sleep", 1.5], ["exit", 0]],
      script=TRIG2 + pal('p0') + ['wait-ext full crashed', 'wait 300'] + pal('p1') + pe('e') + ['quit'], check=chk_prompt_stop)


def chk_palette_cancels(r):
    r.finished()
    r.eq('prompt-was-open', palette(r, 'p0')[0], 'choose=Pick one')
    r.ok('opening-the-palette-answers-cancelled', is_err(r.reply(1), 1, 'cancelled'), 'got %r' % ln_of(r.reply(1)))
    r.ok('the-prompt-is-gone', not (palette(r, 'p1')[0] or '').startswith('choose='), 'print-palette first line %r' % palette(r, 'p1')[0])


plain('choose.opening-the-palette-cancels', ['conf/full'], ops=[WR, WR, CHOOSE3, DONE],
      script=TRIG2 + pal('p0') + ['key ctrl+shift+p', 'wait 300', 'wait 300'] + pal('p1') + ['key Escape', 'wait 300', 'quit'], check=chk_palette_cancels)

# ---------------------------------------------------------------- ask

def ask_scn(name, keys, want_line, want_body, want_field=None):
    def chk(r):
        r.finished()
        head, field, rows, sel = palette(r, 'p0')
        r.eq('prompt-label', head, 'prompt=Your name?')
        r.eq('field-starts-empty', field, '')
        if want_field is not None:
            r.eq('field-shows-what-was-typed', palette(r, 'p1')[1], want_field)
        m = r.reply(1)
        if want_line is None:
            r.ok('escape-answers-cancelled', is_err(m, 1, 'cancelled'), 'got %r' % ln_of(m))
        else:
            r.eq('reply-is-ok-LEN', ln_of(m), want_line)
            r.eq('reply-body-is-the-text', m.body if m else None, want_body)
            r.eq('reply-body-length', getattr(m, 'blen', None), len(want_body))
        r.eq('prompt-closed', palette(r, 'p2')[0], 'none')
    plain('ask.' + name, ['conf/full'], ops=[WR, WR, preq('ask', 'Your name?'), DONE],
          script=TRIG2 + pal('p0') + keys + ['wait 300'] + pal('p1') + [KEYEND, 'wait 300', 'wait 300'] + pal('p2') + ['quit'], check=chk)


KEYEND = 'key Return'
ask_scn('typed-text-comes-back', ['type Bob'], 'ok 1 3', b'Bob', 'Bob')
ask_scn('spaces-and-multibyte-characters', ['type héllo wörld'], 'ok 1 13', 'héllo wörld'.encode(), 'héllo wörld')
ask_scn('empty-answer', [], 'ok 1 0', b'', '')
# Escape instead of Enter: the cancelled variant uses its own end key
plain('ask.escape-cancels', ['conf/full'], ops=[WR, WR, preq('ask', 'Your name?'), DONE],
      script=TRIG2 + pal('p0') + ['type Bob', 'wait 300', 'key Escape', 'wait 300', 'wait 300'] + pal('p2') + ['quit'],
      check=lambda r: (r.finished(), r.eq('prompt-label', palette(r, 'p0')[0], 'prompt=Your name?'),
                       r.ok('escape-answers-cancelled', is_err(r.reply(1), 1, 'cancelled'), 'got %r' % ln_of(r.reply(1))),
                       r.eq('prompt-closed', palette(r, 'p2')[0], 'none')))


def chk_ask_busy(r):
    r.finished()
    r.ok('choose-while-ask-is-open-is-busy', is_err(r.reply(2), 2, 'busy'), 'got %r' % ln_of(r.reply(2)))
    r.eq('ask-prompt-stays', palette(r, 'p0')[0], 'prompt=Your name?')
    r.eq('the-ask-is-answered-by-Enter', ln_of(r.reply(1)), 'ok 1 2')


plain('ask.second-prompt-is-busy', ['conf/full'], ops=[WR, WR, ["batch", [preq('ask', 'Your name?'), CHOOSE3]], DONE],
      script=TRIG2 + ['type ok', 'wait 300'] + pal('p0') + ['key Return', 'wait 300', 'wait 300', 'quit'], check=chk_ask_busy)

# ---------------------------------------------------------------- limits of the prompts (section 7)


def chk_prompt_cut(r):
    r.finished()
    head, field, rows, sel = palette(r, 'p0')
    r.eq('choose-prompt-cut-at-200-bytes-on-a-character-boundary', head, 'choose=a' + 'é' * 99)
    r.eq('items-cut-at-200-bytes', rows, ['b' * 200, 'c' + 'é' * 99])
    r.ok('first-cancelled', is_err(r.reply(1), 1, 'cancelled'), 'got %r' % ln_of(r.reply(1)))
    head, field, rows, sel = palette(r, 'p1')
    r.eq('ask-prompt-cut-at-200-bytes-on-a-character-boundary', head, 'prompt=a' + 'é' * 99)


plain('lim.prompt-and-item-text-is-cut-at-200-bytes', ['conf/full'],
      ops=[WR, WR, preq('choose', 'a' + 'é' * 100, 'b' * 250, 'c' + 'é' * 100), preq('ask', 'a' + 'é' * 100), DONE],
      script=TRIG2 + pal('p0') + ['key Escape', 'wait 300', 'wait 300'] + pal('p1') + ['key Escape', 'wait 300', 'quit'], check=chk_prompt_cut)


def chk_choose_limit(r):
    r.finished()
    r.ok('1001-items-is-toobig', is_err(r.reply(1), 1, 'toobig'), 'got %r' % ln_of(r.reply(1)))
    head, field, rows, sel = palette(r, 'p0')
    r.eq('1000-items-is-accepted', head, 'choose=P')
    r.eq('first-of-the-1000-items-is-selected', sel, 'x')
    r.ok('and-can-be-dismissed', is_err(r.reply(2), 2, 'cancelled'), 'got %r' % ln_of(r.reply(2)))


plain('lim.choose-over-1000-items', ['conf/full'],
      ops=[WR, WR, ["req", "choose", "{LEN}", {"pre": "P\n", "rep": "x\n", "n": 1000, "suf": "x"}],
           ["req", "choose", "{LEN}", {"pre": "P\n", "rep": "x\n", "n": 999, "suf": "x"}], DONE],
      script=TRIG2 + pal('p0') + ['key Escape', 'wait 300', 'quit'], check=chk_choose_limit)


def chk_register_limit(r):
    r.finished()
    bad = [i for i in range(1, 65) if ln_of(r.reply(i)) != 'ok %d' % i]
    r.ok('the-first-64-registers-are-ok', not bad, 'not ok: %r' % [(i, ln_of(r.reply(i))) for i in bad[:5]])
    r.ok('the-65th-is-toobig', is_err(r.reply(65), 65, 'toobig'), 'got %r' % ln_of(r.reply(65)))
    r.ok('and-so-is-the-66th', is_err(r.reply(66), 66, 'toobig'), 'got %r' % ln_of(r.reply(66)))
    e = r.ext('e', 'conf/full')
    cmds = (e['cmds'] or '').split(',') if e else []
    r.eq('the-extension-has-2-manifest-and-64-registered-commands', len(cmds), 66)
    r.ok('extension-still-running', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))


plain('lim.register-65th-is-toobig', ['conf/full'],
      ops=[WR] + [["req", "register", "c%02d - Command %d" % (i, i)] for i in range(1, 67)] + [DONE],
      script=std(pre=[OPEN_A], wait=1800, post=pe('e')), check=chk_register_limit)

# ---------------------------------------------------------------- term

TERM_RUN = "printf 'ext-%s-ok\\n' ran && touch {PROJECT}/term-ran.txt"


def chk_term_needs(r):
    r.finished()
    r.eq('term-needs-terminal', ln_of(r.reply(1)), 'error 1 needs terminal=yes')
    r.eq('the-body-of-the-refused-request-is-skipped', r.reply(2) is not None and r.reply(2).f[0], 'ok')
    r.eq('no-prompt-was-shown', palette(r, 'p')[0], 'none')
    r.ok('extension-still-running', (r.ext('e', 'conf/full') or {}).get('state') == 'running', 'got %r' % (r.ext('e', 'conf/full') or {}).get('raw'))


plain('term.refused-without-terminal-yes', ['conf/full'],
      ops=[WR, ["req", "term", "{LEN}", "echo hi"], ["text", "a.txt"], DONE],
      script=TRIG1 + pal('p') + pe('e') + ['quit'], check=chk_term_needs)

CTRL_STEPS = [
    ('tab-is-syntax', ["req", "term", "{LEN}", "echo a\tb"], ('err', 'syntax')),
    ('ok-1', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('line-feed-inside-is-syntax', ["req", "term", "{LEN}", "echo a\necho b"], ('err', 'syntax')),
    ('ok-2', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('trailing-line-feed-is-syntax', ["req", "term", "{LEN}", "echo a\n"], ('err', 'syntax')),
    ('ok-3', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('carriage-return-is-syntax', ["req", "term", "{LEN}", "echo a\rb"], ('err', 'syntax')),
    ('ok-4', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('escape-is-syntax', ["req", "term", "{LEN}", "echo \x1b[31mred"], ('err', 'syntax')),
    ('ok-5', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('NUL-is-syntax', ["req", "term", "{LEN}", "echo a\x00b"], ('err', 'syntax')),
    ('ok-6', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('bell-is-syntax', ["req", "term", "{LEN}", "echo \x07"], ('err', 'syntax')),
    ('ok-7', ["text", "a.txt"], ('body', A_TXT.encode())),
    ('DEL-is-syntax', ["req", "term", "{LEN}", "echo a\x7fb"], ('err', 'syntax')),
    ('ok-8', ["text", "a.txt"], ('body', A_TXT.encode())),
    DONE,
]
CTRL_OPS, CTRL_CASES = split_steps(CTRL_STEPS)


def chk_term_ctrl(r):
    r.finished()
    check_steps(r, CTRL_STEPS)
    r.eq('no-prompt-was-shown', palette(r, 'p')[0], 'none')
    e = r.ext('e', 'conf/term')
    r.ok('extension-still-running', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))


plain('term.control-characters-are-syntax', ['conf/term'], ops=[WR] + CTRL_OPS,
      script=['open {P}/a.txt', 'cmd term.go', 'wait-ext term running', 'wait 1500'] + pal('p') + pe('e') + ['quit'], check=chk_term_ctrl)


def chk_term_run(r):
    r.finished()
    head, field, rows, sel = palette(r, 'p0')
    r.ok('prompt-is-shown-with-the-command', head is not None and head.startswith('prompt=') and head != 'prompt=', 'first line %r' % head)
    cmd = TERM_RUN.replace('{PROJECT}', r.dirs['proj'])
    r.eq('field-holds-the-command', field, cmd)
    r.ok('nothing-ran-before-Enter', not any('ext-ran-ok' in ln for ln in r.seg('t0')), 'terminal before Enter: %r' % r.seg('t0'))
    r.eq('field-shows-the-edit', palette(r, 'p1')[1], cmd + " && printf 'edit-%s-ok\\n' done")
    r.eq('answered-ok', ln_of(r.reply(1)), 'ok 1')
    r.ok('the-terminal-shows-the-output-of-the-command', any('ext-ran-ok' in ln for ln in r.seg('t1')), 'terminal after Enter: %r' % r.seg('t1')[-8:])
    r.ok('the-edited-command-ran-too', any('edit-done-ok' in ln for ln in r.seg('t1')), 'terminal after Enter: %r' % r.seg('t1')[-8:])
    r.ok('the-command-ran', os.path.exists(os.path.join(r.dirs['proj'], 'term-ran.txt')), 'term-ran.txt does not exist')
    r.eq('prompt-closed', palette(r, 'p2')[0], 'none')


plain('term.shows-edits-and-runs', ['conf/term'], ops=[WR, WR, ["req", "term", "{LEN}", TERM_RUN], DONE],
      script=TRIGT2 + pal('p0') + ['key End', 'key space', "type && printf 'edit-%s-ok\\n' done", 'wait 300'] + pal('p1') + pterm('t0') +
             ['wait 500', 'key Return', 'wait-term edit-done-ok', 'wait 300'] + pterm('t1') + pal('p2') + ['quit'], check=chk_term_run)


def chk_term_esc(r):
    r.finished()
    r.ok('prompt-is-shown', (palette(r, 'p0')[0] or '').startswith('prompt='), 'first line %r' % palette(r, 'p0')[0])
    r.ok('escape-answers-cancelled', is_err(r.reply(1), 1, 'cancelled'), 'got %r' % ln_of(r.reply(1)))
    r.ok('the-command-did-not-run', not os.path.exists(os.path.join(r.dirs['proj'], 'term-esc.txt')), 'term-esc.txt exists')
    r.eq('prompt-closed', palette(r, 'p1')[0], 'none')


plain('term.escape-cancels', ['conf/term'], ops=[WR, WR, ["req", "term", "{LEN}", "touch {PROJECT}/term-esc.txt"], DONE],
      script=TRIGT2 + pal('p0') + ['key Escape', 'wait 300', 'wait 1000'] + pal('p1') + ['quit'], check=chk_term_esc)


def chk_term_empty(r):
    r.finished()
    r.eq('field-was-emptied', palette(r, 'p1')[1], '')
    r.ok('an-empty-line-answers-cancelled', is_err(r.reply(1), 1, 'cancelled'), 'got %r' % ln_of(r.reply(1)))
    r.eq('prompt-closed', palette(r, 'p2')[0], 'none')


plain('term.empty-line-cancels', ['conf/term'], ops=[WR, WR, ["req", "term", "{LEN}", "xy"], DONE],
      # the field shows the command from its start (the cursor is at 0), and the prompt ignores
      # Enter during its first half second
      script=TRIGT2 + ['key Delete', 'key Delete', 'wait 300'] + pal('p1') + ['wait 600', 'key Return', 'wait 300', 'wait 300'] + pal('p2') + ['quit'],
      check=chk_term_empty)


def chk_term_busy(r):
    r.finished()
    for rid in (2, 3, 4):
        r.ok('prompt-%d-while-term-is-open-is-busy' % rid, is_err(r.reply(rid), rid, 'busy'), 'got %r' % ln_of(r.reply(rid)))
    r.ok('term-is-cancelled-by-escape', is_err(r.reply(1), 1, 'cancelled'), 'got %r' % ln_of(r.reply(1)))
    r.ok('term-while-choose-is-open-is-busy', is_err(r.reply(6), 6, 'busy'), 'got %r' % ln_of(r.reply(6)))
    r.ok('choose-is-cancelled-by-escape', is_err(r.reply(5), 5, 'cancelled'), 'got %r' % ln_of(r.reply(5)))


plain('term.second-prompt-is-busy', ['conf/term'],
      ops=[WR, WR, ["batch", [preq('term', 'echo one'), CHOOSE3, preq('ask', 'Your name?'), preq('term', 'echo two')]],
           ["batch", [CHOOSE3, preq('term', 'echo three')]], DONE],
      script=TRIGT2 + ['key Escape', 'wait 300', 'wait 300', 'key Escape', 'wait 300', 'wait 300', 'quit'], check=chk_term_busy)


def chk_term_limit(r):
    r.finished()
    r.ok('4097-bytes-is-toobig', is_err(r.reply(1), 1, 'toobig'), 'got %r' % ln_of(r.reply(1)))
    head, field, rows, sel = palette(r, 'p0')
    r.ok('4096-bytes-is-accepted', head is not None and head.startswith('prompt='), 'first line %r' % head)
    r.eq('field-holds-all-4096-bytes', len(field or ''), 4096)
    r.ok('and-can-be-dismissed', is_err(r.reply(2), 2, 'cancelled'), 'got %r' % ln_of(r.reply(2)))


plain('lim.term-over-4096-bytes', ['conf/term'],
      ops=[WR, WR, ["req", "term", "{LEN}", {"rep": "x", "n": 4097}], ["req", "term", "{LEN}", {"rep": "x", "n": 4096}], DONE],
      script=TRIGT2 + pal('p0') + ['key Escape', 'wait 300', 'quit'], check=chk_term_limit)

# ---------------------------------------------------------------- cmd (section 8a)

CMD_EDIT = {
    'duplicate-line-and-select-all': ([["req", "cmd", "duplicate_line"], ["req", "cmd", "select_all"]], 'alpha\nalpha\nbeta\ngamma\n', True),
    'undo-takes-it-back': ([["req", "cmd", "duplicate_line"], ["req", "cmd", "undo"]], A_TXT, False),
    'redo-does-it-again': ([["req", "cmd", "duplicate_line"], ["req", "cmd", "undo"], ["req", "cmd", "redo"]], 'alpha\nalpha\nbeta\ngamma\n', False),
}


def cmd_edit_scn(name, ops, want_doc, selects):
    def chk(r):
        r.finished()
        for i in range(1, len(ops) + 1):
            r.eq('reply-%d-is-ok' % i, ln_of(r.reply(i)), 'ok %d' % i)
        r.eq('the-command-ran-as-if-the-user-had-invoked-it', r.doc('d'), want_doc)
        if selects:
            r.ok('select_all-selected', r.state('after').get('sel') not in (None, '0'), 'print-state %r' % r.state('after'))
    # the editing commands need documents = write (conf/full has it)
    plain('cmd.' + name, ['conf/full'], ops=[WR] + ops + [DONE],
          script=['open {P}/a.txt', 'cmd full.go', 'wait-ext full running', 'wait 300'] + ps('before') + ['wait 600', 'wait 600'] + ps('after') + pd('d') + ['quit'],
          check=chk)


for _n, (_ops, _doc, _sel) in CMD_EDIT.items():
    cmd_edit_scn(_n, _ops, _doc, _sel)

CMD_LISTED = ('undo redo select_all select_line select_next duplicate_line delete_line move_line_up move_line_down '
              'indent outdent toggle_comment newline_below newline_above find replace find_next find_prev find_in_files goto_line '
              'quick_open command_palette next_tab prev_tab reveal_file focus_explorer toggle_sidebar toggle_agents toggle_terminal '
              'new_terminal clear_terminal toggle_word_wrap toggle_whitespace toggle_line_numbers zoom_in zoom_out zoom_reset').split()


def chk_cmd_listed(r):
    r.finished()
    for i, n in enumerate(CMD_LISTED, 1):
        r.eq('cmd-%s' % n, ln_of(r.reply(i)), 'ok %d' % i)
    st = r.state('s')
    r.ok('the-editor-is-still-there', bool(st), 'print-state after: %r' % r.seg('s'))
    e = r.ext('e', 'conf/term')
    r.ok('extension-still-running', e is not None and e['state'] == 'running', 'got %r' % (e and e['raw']))


# the whole list needs documents = write (the editing commands) and terminal = yes (the panel's)
plain('cmd.every-listed-command-is-ok', ['conf/term'], ops=[WR] + [["req", "cmd", n] for n in CMD_LISTED] + [DONE],
      script=['open {P}/a.txt', 'cmd term.go', 'wait-ext term running', 'wait 2500'] + ps('s') + pe('e') + ['quit'], check=chk_cmd_listed)

CMD_NEEDS_STEPS = [
    ('undo-needs-documents-write', ["req", "cmd", "undo"], ('err', 'needs')),
    ('delete_line-needs-documents-write', ["req", "cmd", "delete_line"], ('err', 'needs')),
    ('toggle_terminal-needs-terminal-yes', ["req", "cmd", "toggle_terminal"], ('err', 'needs')),
    ('select_all-needs-nothing', ["req", "cmd", "select_all"], ('ok',)),
    ('paste-is-not-listed', ["req", "cmd", "paste"], ('err', 'refused')),
    ('copy-is-not-listed', ["req", "cmd", "copy"], ('err', 'refused')),
    ('cut-is-not-listed', ["req", "cmd", "cut"], ('err', 'refused')),
    DONE,
]
CMD_NEEDS_OPS, CMD_NEEDS_CASES = split_steps(CMD_NEEDS_STEPS)


def chk_cmd_needs(r):
    r.finished()
    check_steps(r, CMD_NEEDS_STEPS)
    r.eq('nothing-changed-the-document', r.doc('d'), A_TXT)


plain('cmd.capabilities-gate-the-list', ['conf/nocap'], ops=[WR] + CMD_NEEDS_OPS,
      script=['open {P}/a.txt', 'cmd nocap.go', 'wait-ext nocap running', 'wait 1500'] + pd('d') + ['quit'],
      check=chk_cmd_needs)

CMD_REFUSED_STEPS = [
    ('quit-is-refused', ["req", "cmd", "quit"], ('err', 'refused')),
    ('save-is-refused', ["req", "cmd", "save"], ('err', 'refused')),
    ('close_tab-is-refused', ["req", "cmd", "close_tab"], ('err', 'refused')),
    ('unknown-name-is-refused', ["req", "cmd", "frobnicate"], ('err', 'refused')),
    ('upper-case-of-a-listed-name-is-refused', ["req", "cmd", "SELECT_ALL"], ('err', 'refused')),
    ('extension-command-is-refused', ["req", "cmd", "full.go"], ('err', 'refused')),
    ('no-name-is-syntax', ["req", "cmd"], ('err', 'syntax')),
    ('after', ["text", "a.txt"], ('body', A_TXT.encode() + b'X')),
    DONE,
]
CMD_REFUSED_OPS, CMD_REFUSED_CASES = split_steps(CMD_REFUSED_STEPS)


def chk_cmd_refused(r):
    r.finished()
    check_steps(r, CMD_REFUSED_STEPS)
    st = r.state('s')
    r.ok('quit-did-not-quit-the-editor', st.get('active') == 'a.txt', 'print-state after the request: %r' % r.seg('s'))
    with open(os.path.join(r.dirs['proj'], 'a.txt'), 'rb') as f:
        r.eq('save-did-not-save', f.read(), A_TXT.encode())
    r.eq('close_tab-did-not-close-the-tab', st.get('tabs'), '1')
    r.eq('save-left-the-document-dirty', st.get('dirty'), '1')
    r.eq('the-document-is-still-open', r.doc('d'), A_TXT + 'X')


plain('cmd.refused-names', ['conf/full'], ops=[WR] + CMD_REFUSED_OPS,
      script=['open {P}/a.txt', 'key ctrl+End', 'type X', 'wait 300', 'cmd full.go', 'wait-ext full running', 'wait 1500'] + ps('s') + pd('d') + ['quit'],
      check=chk_cmd_refused)


# @@SCENARIOS-END@@








def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('rhun')
    ap.add_argument('-k', default='', help='run only scenarios whose name contains this')
    ap.add_argument('-j', type=int, default=3)
    ap.add_argument('--quick', action='store_true', help='skip scenarios that take more than 5 s')
    ap.add_argument('--dump', default=None, help='print rhun output and the extension log of one scenario')
    ap.add_argument('--keep', action='store_true')
    a = ap.parse_args()
    rhun = os.path.abspath(a.rhun)
    RHUN['path'] = rhun
    todo = [s for s in SCENARIOS if a.k in s.name and not (a.quick and s.slow)]
    if a.dump:
        todo = [s for s in SCENARIOS if s.name == a.dump]
    results = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=max(1, a.j if not a.dump else 1)) as ex:
        futs = [ex.submit(run_scenario, s, rhun, a.keep or bool(a.dump)) for s in todo]
        for f in futs:
            results.append(f.result())
    failed = 0
    total = 0
    for res in results:
        if a.dump:
            print('--- rhun stdout'); print(res.stdout); print('--- rhun stderr'); print(res.err)
            print('--- ext log')
            for r in res.recs:
                print('%.3f %s %s' % (r.ts - res.recs[0].ts, r.kind, r.text[:300]))
            print('--- dirs', res.dirs, 'rc', res.rc, 'elapsed %.2f' % res.elapsed)
        for name, ok, msg in res.checks:
            total += 1
            if ok:
                print('ok ' + name)
            else:
                failed += 1
                print('FAIL %s: %s' % (name, msg))
        sys.stdout.flush()
    print('%d checks, %d failed' % (total, failed), file=sys.stderr)
    sys.exit(1 if failed else 0)


if __name__ == '__main__':
    main()
