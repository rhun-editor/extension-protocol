#!/usr/bin/env python3
"""Developer generator checks. RHUN_UCD_ZIP enables pinned-source regeneration tests."""
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT=Path(__file__).resolve().parent.parent
spec=importlib.util.spec_from_file_location('snippet_unicode',ROOT/'tools/snippet_unicode.py')
u=importlib.util.module_from_spec(spec);spec.loader.exec_module(u)

class Generator(unittest.TestCase):
    def test_interval_algebra(self):
        self.assertEqual(u.normalize([(5,6),(1,3),(3,4),(9,9)]),[(1,6),(9,9)])
        self.assertEqual(u.subtract([(0,10),(20,30)],[(3,4),(6,23),(25,26)]),[(0,2),(5,5),(24,24),(27,30)])
        self.assertEqual(u.scalars([(0,0x10ffff)]),u.SCALARS)
        for bad in [[(-1,1)],[(2,1)],[(0,0x110000)]]:
            with self.assertRaises(ValueError):u.normalize(bad)
    def test_unicode_data_ranges(self):
        a=['3400','<CJK Ideograph Extension A, First>','Lo','0','L','','','','','N','','','','','']
        b=a.copy();b[0]='4DBF';b[1]='<CJK Ideograph Extension A, Last>'
        text=';'.join(a)+'\n'+';'.join(b)+'\n'
        result=list(u.data_records(text));self.assertEqual(result[0][:2],(0x3400,0x4dbf))
        for bad in [';'.join(a),';'.join(b),';'.join(b)+'\n'+';'.join(a)]:
            with self.assertRaises(ValueError):list(u.data_records(bad))
        b[2]='Lu'
        with self.assertRaises(ValueError):list(u.data_records(';'.join(a)+'\n'+';'.join(b)))
    def test_bad_input_leaves_outputs(self):
        with tempfile.TemporaryDirectory() as tmp:
            path=Path(tmp);bad=path/'bad.zip';bad.write_bytes(b'not a zip')
            go=path/'out.go';asm=path/'out.s';go.write_text('keep Go');asm.write_text('keep ASM')
            run=subprocess.run([sys.executable,str(ROOT/'tools/snippet_unicode.py'),str(bad),'--go-out',str(go),'--asm-out',str(asm)],capture_output=True)
            self.assertNotEqual(run.returncode,0);self.assertEqual(go.read_text(),'keep Go');self.assertEqual(asm.read_text(),'keep ASM')
            # A same-sized impostor must fail the cryptographic pin as well.
            bad.write_bytes(bytes(9020779))
            with self.assertRaisesRegex(ValueError,'SHA256'):u.read_ucd(bad)
    @unittest.skipUnless(os.environ.get('RHUN_UCD_ZIP'),'set RHUN_UCD_ZIP for source checks')
    def test_pinned_source_and_deterministic_regeneration(self):
        source=os.environ['RHUN_UCD_ZIP'];model=u.build_model(u.read_ucd(source))
        props,aliases,fold,upper,lower=model
        self.assertEqual(len(u.BINARY),53)
        self.assertEqual(fold[0x212a],ord('k'));self.assertEqual(upper[0xdf],(83,83));self.assertEqual(lower[0x130],(105,0x307))
        self.assertEqual(aliases['scx:Hira'],'scx:Hiragana');self.assertNotIn('bin:CE',aliases)
        contains=lambda key,cp:any(a<=cp<=b for a,b in props[key])
        self.assertTrue(contains('scx:Hiragana',0x30fc));self.assertTrue(contains('scx:Katakana',0x30fc));self.assertFalse(contains('scx:Common',0x30fc))
        self.assertTrue(contains('gc:L',0x4e00));self.assertFalse(contains('gc:L',0x345))
        self.assertTrue(contains('gc:Lu',0x10d50));self.assertTrue(contains('gc:Ll',0x10d70))
        self.assertFalse(contains('gc:Lu',0x1c5));self.assertFalse(contains('gc:Ll',0x1c5))
        self.assertFalse(contains('sc:Hiragana',0x30fc));self.assertTrue(contains('sc:Unknown',0x378));self.assertEqual(props['gc:Cs'],[])
        with tempfile.TemporaryDirectory() as tmp:
            path=Path(tmp);pairs=[]
            for i in range(2):
                go,asm=path/f'{i}.go',path/f'{i}.s'
                subprocess.run([sys.executable,str(ROOT/'tools/snippet_unicode.py'),source,'--go-out',str(go),'--asm-out',str(asm)],check=True)
                pairs.append((go.read_bytes(),asm.read_bytes()))
            self.assertEqual(pairs[0],pairs[1])
            for name,key in [('letter','gc:L'),('upper_letter','gc:Lu'),('lower_letter','gc:Ll')]:
                table='\n'.join('.long 0x%x,0x%x'%r for r in props[key])
                self.assertIn(('.Lsu_'+name+':\n'+table+'\n').encode(),pairs[0][1])
                self.assertIn(('FN snippet_unicode_'+name+'\n').encode(),pairs[0][1])
            self.assertEqual(pairs[0][0],(ROOT/'go/rhunext/snippet_unicode_data.go').read_bytes())

if __name__=='__main__':unittest.main()
