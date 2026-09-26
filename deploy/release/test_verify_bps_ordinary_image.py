import argparse
import json
import os
import pathlib
import tempfile
import unittest

from verify_bps import Failure
from verify_bps_ordinary_image import OrdinaryImageVerifier, diagnose


def reply(text):
    return {'output': [{'type': 'message', 'role': 'assistant',
                        'content': [{'type': 'output_text', 'text': text}]}],
            'usage': {'input_tokens': 2, 'output_tokens': 3}}


class DiagnosisTests(unittest.TestCase):
    def test_exact_match_is_distinct_from_formatting(self):
        self.assertEqual(diagnose(reply('ABCDEF01'), 'ABCDEF01')['classification'], 'exact_match')
        for text in ('abcdef01', 'The code is ABCDEF01.', '```\nABCDEF01\n```'):
            result = diagnose(reply(text), 'ABCDEF01')
            self.assertTrue(result['correct'])
            self.assertFalse(result['strict_match'])
            self.assertEqual(result['classification'], 'correct_content_format_mismatch')

    def test_wrong_code_is_never_accepted(self):
        result = diagnose(reply('ABCDEF02'), 'ABCDEF01')
        self.assertFalse(result['correct'])
        self.assertEqual(result['classification'], 'incorrect_code')

    def test_unreadable_statement_is_never_accepted(self):
        result = diagnose(reply('I cannot see the image. ABCDEF01'), 'ABCDEF01')
        self.assertFalse(result['correct'])
        self.assertEqual(result['classification'], 'unreadable_or_refused')

    def test_previous_turn_echo_has_its_own_diagnosis(self):
        result = diagnose(reply('12345678'), 'ABCDEF01', '12345678')
        self.assertEqual(result['classification'], 'previous_text_echo')

    def test_mismatch_preserves_evidence_and_checks_transport_and_billing(self):
        with tempfile.TemporaryDirectory() as directory:
            args = argparse.Namespace(output=str(pathlib.Path(directory) / 'report.json'))

            class Fake(OrdinaryImageVerifier):
                def setup(self):
                    pass

                def infer(self, history, tools=None):
                    self.calls += 1
                    self.report['requests'] = self.calls
                    text = history[0]['content'][0]['text'].removeprefix('Reply with exactly ') if self.calls == 1 else 'Cannot read this image.'
                    result = reply(text)
                    self.report['response_diagnostics'].append({'request': self.calls})
                    return result

                def check_assets(self):
                    self.report['assets_checked'] = True

                def reconcile(self, start, responses):
                    self.report['reconciled_responses'] = len(responses)

                def cleanup(self):
                    self.report['cleaned'] = True
                    return True

            verifier = Fake(args)
            report = verifier.execute()
            self.assertEqual(report['status'], 'failed')
            self.assertEqual(report['failure'], 'ordinary_image_ocr_mismatch')
            self.assertTrue(report['assets_checked'])
            self.assertEqual(report['reconciled_responses'], 2)
            self.assertTrue(report['cleaned'])
            private = pathlib.Path(args.output + '.private')
            self.assertEqual(private.stat().st_mode & 0o777, 0o700)
            evidence = json.loads((private / '02-ordinary-image.json').read_text())
            self.assertEqual(evidence['reply_text'], 'Cannot read this image.')
            self.assertEqual(len(evidence['expected']), 8)
            self.assertTrue((private / '02-ordinary-image.png').read_bytes().startswith(b'\x89PNG'))
            for path in private.iterdir():
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertNotIn(evidence['reply_text'], json.dumps(report))
            self.assertNotIn(evidence['expected'], json.dumps(report))

    def test_existing_evidence_is_never_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            args = argparse.Namespace(output=str(pathlib.Path(directory) / 'report.json'))
            os.mkdir(args.output + '.private', 0o700)
            verifier = OrdinaryImageVerifier(args)
            report = verifier.execute()
            self.assertEqual(report['failure'], 'evidence_directory_exists')
            self.assertEqual(verifier.calls, 0)


if __name__ == '__main__':
    unittest.main()
