#!/usr/bin/env python3
"""Scripted fake extension for the rhun host conformance suite (protocol v1).

Standard library only.  One program, many behaviours, driven by the environment:

  CONF_LOG            file that receives one record per line (required for checks)
  CONF_SCENARIO       "ops" (default) runs CONF_OPS; others are named below
  CONF_OPS            JSON list of operations (see run_ops) used by scenario "ops"
  CONF_OPS_NAME       the same for the extension called NAME (upper case), when several run at once
  CONF_HELLO          first bytes to write instead of "hello 1\\n" (backslash escapes allowed)
  CONF_HELLO_DELAY    seconds to wait before the hello
  CONF_NOHELLO=1      never say hello
  CONF_CRLF=1         end every line we write with CR LF
  CONF_FRAG=1         write everything one byte at a time
  CONF_IGNORE_BYE=1   do not leave on `0 bye`
  CONF_IGNORE_TERM=1  ignore SIGTERM
  CONF_IGNORE_EOF=1   keep running when standard input ends
  CONF_HEARTBEAT=1    log "alive" every 100 ms, so the runner can tell when the process died
  CONF_SLOT_EXIT=SUF  scenario "slot": the extension whose name ends in SUF exits after 1.5 s

Log record format: "<epoch-seconds> <KIND> <text>" where KIND is
  <  a line received from rhun (CR and non-ASCII escaped)
  >  a line we sent
  B  a body received:  LEN SHA256 JSON|-   (JSON string when LEN <= 4096)
  S  a body we sent:   LEN SHA256 JSON|-
  E  an environment variable:  NAME JSON-value|null (unset)
  X  a note about what the extension did (start, pid, mark, exit ...)
"""
import hashlib
import json
import os
import select
import signal
import subprocess
import sys
import threading
import time

ENV = os.environ
LOGFD = None
if ENV.get('CONF_LOG'):
    # a confined extension may write only in its state folder: the log goes there when rhun says
    # where it is, and run.py gathers every extension's log by time
    path = os.path.join(ENV['RHUN_STATE'], 'conf.log') if ENV.get('RHUN_STATE') else ENV['CONF_LOG']
    LOGFD = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o644)
LOCK = threading.RLock()


def esc(b):
    if isinstance(b, str):
        b = b.encode('utf-8', 'surrogateescape')
    s = b.decode('utf-8', 'backslashreplace')
    return s.replace('\r', '\\r').replace('\n', '\\n')


def log(kind, text=''):
    if LOGFD is None:
        return
    line = '%.3f %s %s\n' % (time.time(), kind, text)
    with LOCK:
        os.write(LOGFD, line.encode('utf-8', 'backslashreplace'))


def body_record(kind, data):
    sha = hashlib.sha256(data).hexdigest()
    js = '-'
    if len(data) <= 4096:
        js = json.dumps(data.decode('utf-8', 'surrogateescape'))
    log(kind, '%d %s %s' % (len(data), sha, js))


def finish(reason, code=0):
    log('X', 'exit %s code=%d' % (reason, code))
    os._exit(code)


# --------------------------------------------------------------------------- signals

def on_term(signum, frame):
    log('X', 'sigterm')
    os._exit(0)


if ENV.get('CONF_IGNORE_TERM') == '1':
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
else:
    signal.signal(signal.SIGTERM, on_term)
signal.signal(signal.SIGPIPE, signal.SIG_IGN)


# --------------------------------------------------------------------------- input

class Conn:
    def __init__(self):
        self.buf = bytearray()

    def fill(self, timeout):
        """Read more bytes; False on timeout; raises EOFError at end of input."""
        if timeout is not None:
            r, _, _ = select.select([0], [], [], max(timeout, 0))
            if not r:
                return False
        d = os.read(0, 1 << 20)
        if not d:
            raise EOFError
        self.buf += d
        return True

    def readline(self, timeout):
        end = None if timeout is None else time.time() + timeout
        while True:
            i = self.buf.find(b'\n')
            if i >= 0:
                line = bytes(self.buf[:i])
                del self.buf[:i + 1]
                return line
            left = None if end is None else end - time.time()
            if left is not None and left <= 0:
                return None
            if not self.fill(left):
                return None

    def readexact(self, n):
        while len(self.buf) < n:
            self.fill(None)
        data = bytes(self.buf[:n])
        del self.buf[:n]
        return data


class Reply:
    def __init__(self, line, body=None):
        self.line = line
        self.body = body
        f = line.split(' ')
        self.ok = f[0] == 'ok'
        self.id = int(f[1])
        self.code = None if self.ok else (f[2] if len(f) > 2 else '')
        self.args = f[2:] if self.ok else f[3:]
        self.text = ' '.join(self.args)


class Doc:
    def __init__(self, ver, lang, path):
        self.ver = ver
        self.prev = ver
        self.lang = lang
        self.path = path

    def set_ver(self, v):
        if v != self.ver:
            self.prev = self.ver
            self.ver = v


# --------------------------------------------------------------------------- extension

class Ext:
    def __init__(self):
        self.conn = Conn()
        self.docs = {}
        self.by_name = {}
        self.active = None
        self.project = None
        self.runs = []
        self.last_run_doc = None
        self.replies = {}
        self.pending = {}
        self.next_id = 1
        self.bye = False
        self.frag = ENV.get('CONF_FRAG') == '1'
        self.crlf = ENV.get('CONF_CRLF') == '1'
        self.ignore_bye = ENV.get('CONF_IGNORE_BYE') == '1'
        self.children = []
        self.broken = False

    # ---- output
    def write_bytes(self, data, frag=None, split=None):
        if self.broken:
            return
        frag = self.frag if frag is None else frag
        try:
            if split:
                n, pause = split
                self._write(data[:n], frag)
                log('X', 'sent-part %d' % n)
                time.sleep(pause)
                self._write(data[n:], frag)
                log('X', 'sent-rest %d' % (len(data) - n))
            else:
                self._write(data, frag)
        except (BrokenPipeError, OSError) as e:
            self.broken = True
            log('X', 'stdout-broken %s' % type(e).__name__)

    def _write(self, data, frag):
        if frag:
            for i in range(len(data)):
                os.write(1, data[i:i + 1])
                time.sleep(0.0004)
            return
        mv = memoryview(data)
        while len(mv):
            n = os.write(1, mv)
            mv = mv[n:]

    def eol(self):
        return '\r\n' if self.crlf else '\n'

    # ---- input
    def pump(self, timeout):
        line = self.conn.readline(timeout)
        if line is None:
            return None
        text = line.decode('utf-8', 'surrogateescape')
        log('<', esc(line))
        f = text.split(' ')
        if f[0] == '0':
            self.event(text)
            return text
        if f[0] in ('ok', 'error'):
            rid = int(f[1])
            body = None
            verb = self.pending.pop(rid, None)
            if f[0] == 'ok' and verb in ('text', 'range') and len(f) >= 4:
                body = self.conn.readexact(int(f[3]))
                body_record('B', body)
            elif f[0] == 'ok' and verb == 'ask' and len(f) >= 3:       # ok ID LEN + the answer
                body = self.conn.readexact(int(f[2]))
                body_record('B', body)
            r = Reply(text.rstrip('\r'), body)
            if r.ok and verb in ('text', 'range', 'replace', 'insert') and r.args:
                d = self.docs.get(self.req_doc.get(rid))
                if d is not None:
                    d.set_ver(int(r.args[0]))
            self.replies[rid] = r
            return text
        return text

    req_doc = {}

    def event(self, text):
        f = text.split(' ')
        name = f[1] if len(f) > 1 else ''
        try:
            if name == 'ready':
                self.project = text.split(' ', 2)[2]
            elif name == 'opened':
                g = text.split(' ', 5)
                doc = int(g[2])
                self.docs[doc] = Doc(int(g[3]), g[4], g[5])
                self.by_name[os.path.basename(g[5])] = doc
            elif name == 'active':
                self.active = int(f[2])
            elif name in ('changed', 'saved'):
                d = self.docs.get(int(f[2]))
                if d:
                    d.set_ver(int(f[3]))
            elif name == 'closed':
                self.docs.pop(int(f[2]), None)
            elif name == 'run':
                self.runs.append((f[2], int(f[3])))
                self.last_run_doc = int(f[3])
            elif name == 'bye':
                self.bye = True
                if not self.ignore_bye:
                    finish('bye')
        except (IndexError, ValueError):
            log('X', 'unparsed-event')

    def settle(self, idle=0.4, maxt=5.0):
        end = time.time() + maxt
        while time.time() < end:
            if self.pump(idle) is None:
                break

    def wait_reply(self, rid, timeout=8.0):
        end = time.time() + timeout
        while rid not in self.replies:
            if 0 in self.replies:       # `error 0 ...`: the host could not tie the error to this request
                log('X', 'error-0 taken as the reply to %d' % rid)
                self.pending.pop(rid, None)
                return self.replies.pop(0)
            left = end - time.time()
            if left <= 0:
                log('X', 'timeout waiting for reply %d' % rid)
                return None
            self.pump(left)
        return self.replies.pop(rid)

    # ---- tokens
    def doc(self, tok):
        if isinstance(tok, int):
            return tok
        if tok == '@active':
            return self.active if self.active is not None else 0
        if tok == '@run':
            return self.last_run_doc if self.last_run_doc is not None else 0
        if tok not in self.by_name:
            self.settle(0.3, 2.0)
        if tok in self.by_name:
            return self.by_name[tok]
        log('X', 'unknown-doc-name %s' % tok)
        return 424242

    def ver(self, doc, tok):
        d = self.docs.get(doc)
        if isinstance(tok, int):
            return tok
        if tok == 'cur':
            return d.ver if d else 1
        if tok == 'old':
            return d.prev if d else 1
        raise ValueError(tok)

    def sub(self, s):
        if isinstance(s, str):
            s = (s.replace('{PROJECT}', self.project or '-')
                  .replace('{STATE}', ENV.get('RHUN_STATE', '')))
            while '{DOC:' in s:
                i = s.index('{DOC:')
                j = s.index('}', i)
                s = s[:i] + str(self.doc(s[i + 5:j])) + s[j + 1:]
        return s

    def body(self, b):
        if b is None:
            return b''
        if isinstance(b, str):
            return self.sub(b).encode('utf-8')
        if 'rep' in b:
            return (b.get('pre', '') + b['rep'] * b['n'] + b.get('suf', '')).encode('utf-8')
        if 'hex' in b:
            return bytes.fromhex(b['hex'])
        raise ValueError(b)

    # ---- building messages
    def build(self, op):
        """Return (id, header, body, verb, doc, opts)."""
        name = op[0]
        opts = op[-1] if isinstance(op[-1], dict) and 'rep' not in op[-1] and 'hex' not in op[-1] else {}
        if opts:
            op = op[:-1]
        rid = opts.get('id', self.next_id)
        self.next_id = max(self.next_id, rid) + 1
        doc = None
        body = b''
        if name == 'text':
            doc = self.doc(op[1]); head = 'text %d' % doc
        elif name == 'range':
            doc = self.doc(op[1]); head = 'range %d %d %d %d %d' % ((doc,) + tuple(op[2:6]))
        elif name == 'replace':
            doc = self.doc(op[1]); v = self.ver(doc, op[2]); body = self.body(op[7])
            n = opts.get('len', len(body))
            head = 'replace %d %d %d %d %d %d %d' % ((doc, v) + tuple(op[3:7]) + (n,))
        elif name == 'insert':
            doc = self.doc(op[1]); v = self.ver(doc, op[2]); body = self.body(op[3])
            head = 'insert %d %d %d' % (doc, v, opts.get('len', len(body)))
        elif name == 'select':
            doc = self.doc(op[1]); head = 'select %d %d %d %d %d' % ((doc,) + tuple(op[2:6]))
        elif name in ('toast', 'status'):
            body = self.body(op[1]); head = '%s %d' % (name, opts.get('len', len(body)))
        elif name == 'open':
            head = 'open %d %d %s' % (op[1], op[2], self.sub(op[3]))
        elif name == 'cancel':
            head = 'cancel %d' % op[1]
        elif name == 'req':
            head = op[1] + ((' ' + self.sub(op[2])) if len(op) > 2 and op[2] != '' else '')
            if len(op) > 3:
                body = self.body(op[3])
            head = head.replace('{LEN}', str(len(body)))        # the real length of the body, after substitutions
        else:
            raise ValueError('unknown op ' + name)
        verb = head.split(' ')[0]
        return rid, head, body, verb, doc, opts

    def message_bytes(self, built):
        rid, head, body, verb, doc, opts = built
        data = ('%d %s' % (rid, head)).encode('utf-8') + self.eol().encode() + body
        return data

    def send_built(self, built):
        rid, head, body, verb, doc, opts = built
        self.pending[rid] = verb
        self.req_doc[rid] = doc
        log('>', esc('%d %s' % (rid, head)))
        if body:
            body_record('S', body)
        split = opts.get('split')
        if split and len(split) == 3 and split[2] == 'body':      # [bytes into the body, pause, "body"]
            head_len = len(('%d %s' % (rid, head)).encode('utf-8') + self.eol().encode())
            split = (head_len + split[0], split[1])
        self.write_bytes(self.message_bytes(built), frag=opts.get('frag'), split=split)

    # ---- operations
    def run_ops(self, ops):
        for op in ops:
            self.do_op(op)

    def do_op(self, op):
        name = op[0]
        if name == 'sleep':
            time.sleep(op[1])
        elif name == 'drain':
            self.settle(op[1], 5.0)
        elif name == 'mark':
            log('X', 'mark %s' % op[1])
        elif name == 'waitrun':
            end = time.time() + (op[1] if len(op) > 1 else 20)
            while not self.runs and time.time() < end:
                self.pump(max(0.05, end - time.time()))
            if self.runs:
                log('X', 'got-run %s %d' % self.runs.pop(0))
            else:
                log('X', 'no-run')
        elif name == 'exit':
            finish('requested', op[1])
        elif name == 'close':
            # The host can kill the process as soon as close returns. Record the start of the
            # operation first so an immediate successful termination cannot erase its marker.
            log('X', 'closing %s' % op[1])
            os.close(1 if op[1] == 'stdout' else 0)
            log('X', 'closed %s' % op[1])
        elif name == 'spawn':
            p = subprocess.Popen(['sleep', '120'], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL)
            self.children.append(p)
            log('X', 'child %d' % p.pid)
        elif name == 'heartbeat':
            def beat():
                while True:
                    log('X', 'alive')
                    time.sleep(0.1)
            threading.Thread(target=beat, daemon=True).start()
        elif name == 'noread':
            log('X', 'noread %s' % op[1])
            time.sleep(op[1])
            log('X', 'noread-done')
        elif name == 'stderr':
            n = op[1]
            chunk = ('x' * 1023 + '\n').encode()
            sent = 0
            while sent < n:
                os.write(2, chunk)
                sent += len(chunk)
            log('X', 'stderr-written %d' % sent)
        elif name == 'stderrmsg':
            os.write(2, (op[1] + '\n').encode())
        elif name == 'raw':
            data = self.body(op[1])
            log('>', 'raw %d bytes %s' % (len(data), esc(data[:80])))
            self.write_bytes(data)
        elif name == 'writefile':       # the extension edits a file behind rhun's back
            with open(self.sub(op[1]), 'wb') as f:
                f.write(self.body(op[2]))
            log('X', 'wrote %s' % self.sub(op[1]))
        elif name == 'ignore':
            if op[1] == 'bye':
                self.ignore_bye = True
            elif op[1] == 'term':
                signal.signal(signal.SIGTERM, signal.SIG_IGN)
        elif name == 'frag':
            self.frag = bool(op[1])
        elif name == 'batch':       # one write, then every reply in order
            built = [self.build(o) for o in op[1]]
            for b in built:
                self.pending[b[0]] = b[3]
                self.req_doc[b[0]] = b[4]
                log('>', esc('%d %s' % (b[0], b[1])))
                if b[2]:
                    body_record('S', b[2])
            log('X', 'batch-write %d' % len(built))
            self.write_bytes(b''.join(self.message_bytes(b) for b in built))
            for b in built:
                self.wait_reply(b[0])
        elif name == 'send':        # one write, do not read the replies
            built = [self.build(o) for o in op[1]]
            for b in built:
                self.pending[b[0]] = b[3]
                self.req_doc[b[0]] = b[4]
                log('>', esc('%d %s' % (b[0], b[1])))
            log('X', 'send-write %d' % len(built))
            self.write_bytes(b''.join(self.message_bytes(b) for b in built))
        elif name == 'flood':
            self.flood(op[1], op[2] if len(op) > 2 else 'cancel 1')
        else:
            built = self.build(op)
            self.send_built(built)
            if not built[5].get('nowait'):
                self.wait_reply(built[0])

    def flood(self, count, tail):
        stop = threading.Event()
        sent = [0]

        def writer():
            i = 1
            try:
                while i <= count and not stop.is_set():
                    chunk = []
                    for _ in range(500):
                        if i > count:
                            break
                        chunk.append('%d %s\n' % (i, tail))
                        i += 1
                    self._write(''.join(chunk).encode(), False)
                    sent[0] = i - 1
            except (BrokenPipeError, OSError):
                log('X', 'flood-epipe')
            log('X', 'flood-sent %d' % sent[0])
            stop.set()

        t = threading.Thread(target=writer, daemon=True)
        t.start()
        got = 0
        try:
            while not stop.is_set():
                line = self.conn.readline(0.2)
                if line is not None:
                    got += 1
        except EOFError:
            log('X', 'flood-eof replies=%d' % got)
            finish('eof')
        # drain what is left so the replies are counted
        try:
            while True:
                line = self.conn.readline(1.0)
                if line is None:
                    break
                got += 1
                if line.startswith(b'0 bye'):
                    break
        except EOFError:
            pass
        log('X', 'flood-replies %d' % got)

    # ---- main loop
    def hold(self):
        """Read forever; leave on bye or end of input."""
        while True:
            try:
                self.pump(None)
            except EOFError:
                if ENV.get('CONF_IGNORE_EOF') == '1':
                    log('X', 'eof-ignored')
                    time.sleep(1000)
                finish('eof')


def log_env():
    for k in ('RHUN_VERSION', 'RHUN_PROTOCOL', 'RHUN_EXTENSION', 'RHUN_TARGET', 'RHUN_STATE',
              'RHUN_PROJECT', 'RHUN_SOCKET', 'HOME', 'TMPDIR', 'XDG_CACHE_HOME', 'GOCACHE',
              'GOMODCACHE', 'GOPATH', 'npm_config_cache', 'LANG', 'LC_ALL', 'TZ', 'PATH',
              'CONF_MARK'):
        log('E', '%s %s' % (k, json.dumps(ENV.get(k))))
    log('E', 'KEYS %s' % json.dumps(sorted(ENV.keys())))
    log('X', 'cwd %s' % json.dumps(os.getcwd()))
    log('X', 'argv %s' % json.dumps(sys.argv))
    log('X', 'pid %d pgid %d' % (os.getpid(), os.getpgid(0)))
    tmp = ENV.get('TMPDIR')
    log('X', 'tmpdir-exists %s' % (tmp is not None and os.path.isdir(tmp)))
    log('X', 'start %s' % ENV.get('RHUN_EXTENSION'))


def main():
    x = Ext()
    log_env()
    os.write(2, b'ext-stderr-marker\n')
    if ENV.get('CONF_HEARTBEAT') == '1':
        x.do_op(['heartbeat'])
    sc = ENV.get('CONF_SCENARIO', 'ops')
    if ENV.get('CONF_HELLO_DELAY'):
        time.sleep(float(ENV['CONF_HELLO_DELAY']))
    if ENV.get('CONF_NOHELLO') == '1':
        log('X', 'no hello on purpose')
        x.hold()
    hello = 'hello 1' + x.eol()
    if ENV.get('CONF_HELLO'):
        hello = ENV['CONF_HELLO'].encode().decode('unicode_escape')
    log('>', esc(hello.rstrip('\n')))
    x.write_bytes(hello.encode('utf-8'))
    log('X', 'hello-sent')
    try:
        if sc == 'slot':
            suf = ENV.get('CONF_SLOT_EXIT', '')
            x.settle()
            if suf and ENV.get('RHUN_EXTENSION', '').endswith(suf):
                time.sleep(1.5)
                finish('slot-exit', 0)
            x.hold()
        if ENV.get('CONF_EXIT_AFTER_HELLO'):
            finish('exit-after-hello', int(ENV['CONF_EXIT_AFTER_HELLO']))
        if ENV.get('CONF_NOSETTLE') != '1':
            x.settle()
        ops = json.loads(ENV.get('CONF_OPS_' + ENV.get('RHUN_EXTENSION', '').split('/')[-1].upper()) or ENV.get('CONF_OPS', '[]'))
        x.run_ops(ops)
        x.hold()
    except EOFError:
        finish('eof')


if __name__ == '__main__':
    main()
