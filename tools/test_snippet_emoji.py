#!/usr/bin/env python3
"""Generator checks. RHUN_EMOJI_SEQUENCES and RHUN_EMOJI_ZWJ enable pin checks."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import snippet_emoji as emoji

ROOT = Path(__file__).resolve().parent.parent


class Generator(unittest.TestCase):
    def test_records(self):
        rows = list(emoji.records('1F600..1F601 ; Basic_Emoji ; faces # comment\n'
                                 '0031 FE0F 20E3 ; Emoji_Keycap_Sequence ; key', emoji.COUNTS))
        self.assertEqual(rows, [('Basic_Emoji', '\U0001f600'), ('Basic_Emoji', '\U0001f601'),
                                ('Emoji_Keycap_Sequence', '1\ufe0f\u20e3')])
        for value in ['D800', '110000', '1f600', '0030..0020', '0000..2000',
                      'D7FF..E000', '0030..0031 0032', '', '0030 ' * 11]:
            with self.subTest(value=value), self.assertRaises(ValueError):
                list(emoji.records(value + ' ; Basic_Emoji ; bad', emoji.COUNTS))
        for value in ['0030 ; Unknown ; bad', '0030 ; Basic_Emoji',
                      '0030 ; Basic_Emoji ; extra ; field']:
            with self.assertRaises(ValueError):
                list(emoji.records(value, emoji.COUNTS))

    def test_counts_duplicates_and_determinism(self):
        text = '1F601 ; Basic_Emoji ; face\n1F600 ; Basic_Emoji ; face'
        model = emoji.build_model(text, '', check_counts=False)
        self.assertEqual(model['RGI_Emoji'], ['\U0001f600', '\U0001f601'])
        self.assertEqual(emoji.emit_go(model), emoji.emit_go(dict(reversed(list(model.items())))))
        self.assertIn('\\U0001f600', emoji.emit_go(model))
        with self.assertRaisesRegex(ValueError, 'counts'):
            emoji.build_model(text, '')
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            emoji.build_model(text + '\n' + text, '', check_counts=False)
        with self.assertRaisesRegex(ValueError, 'property'):
            emoji.build_model('', text, check_counts=False)

    def test_bad_input_preserves_output(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp)
            source, zwj, out = [path / name for name in ['sequences', 'zwj', 'out.go']]
            source.write_bytes(b'bad')
            zwj.write_bytes(b'bad')
            out.write_text('keep')
            result = subprocess.run([sys.executable, str(ROOT / 'tools/snippet_emoji.py'),
                                     str(source), str(zwj), '--go-out', str(out)], capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(out.read_text(), 'keep')
            source.write_bytes(bytes(emoji.SOURCES['sequences'][0]))
            with self.assertRaisesRegex(ValueError, 'SHA256'):
                emoji.read_source(source, 'sequences')
            result = subprocess.run([sys.executable, str(ROOT / 'tools/snippet_emoji.py'),
                                     str(source), str(zwj), '--go-out', str(source)], capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(source.stat().st_size, emoji.SOURCES['sequences'][0])

    @unittest.skipUnless(os.environ.get('RHUN_EMOJI_SEQUENCES') and os.environ.get('RHUN_EMOJI_ZWJ'),
                         'set RHUN_EMOJI_SEQUENCES and RHUN_EMOJI_ZWJ for source checks')
    def test_pinned_regeneration(self):
        model = emoji.build_model(emoji.read_source(os.environ['RHUN_EMOJI_SEQUENCES'], 'sequences'),
                                  emoji.read_source(os.environ['RHUN_EMOJI_ZWJ'], 'zwj'))
        self.assertEqual(len(model['RGI_Emoji']), 3790)
        self.assertEqual(max(map(len, model['RGI_Emoji'])), 10)
        self.assertIn('\U0001f3f3\ufe0f\u200d\U0001f308', model['RGI_Emoji'])
        with tempfile.TemporaryDirectory() as tmp:
            outputs = []
            for i in range(2):
                out = Path(tmp) / f'{i}.go'
                subprocess.run([sys.executable, str(ROOT / 'tools/snippet_emoji.py'),
                                os.environ['RHUN_EMOJI_SEQUENCES'], os.environ['RHUN_EMOJI_ZWJ'],
                                '--go-out', str(out)], check=True)
                outputs.append(out.read_bytes())
            self.assertEqual(outputs[0], outputs[1])
            self.assertEqual(outputs[0], (ROOT / 'go/rhunext/snippet_emoji_data.go').read_bytes())


if __name__ == '__main__':
    unittest.main()
