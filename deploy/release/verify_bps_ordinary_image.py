#!/usr/bin/env python3
"""Two BPS calls: text nonce followed by an ordinary user image.

Preserve the synthetic PNG, expected code and reply text in owner-only sidecars
before assertions. Public reports contain classifications, hashes and usage only.
Exact OCR remains the acceptance gate; formatting is diagnosed separately.
Run beside verify_bps.py and verify_bps_tool_image.py under exclusive settings
writer ownership. No upstream retries or account/model switching.
"""
from verify_bps_tool_image import *


def raw_text(response):
    return ''.join(c.get('text', '') for o in response.get('output', [])
                   if o.get('type') == 'message' for c in o.get('content', [])
                   if c.get('type') == 'output_text')


def diagnose(response, expected, previous=None):
    result = classify(response, expected)
    text = raw_text(response)
    result.update(strict_match=text == expected, reply_length=len(text),
                  reply_sha256=hashlib.sha256(text.encode()).hexdigest())
    if result['refusal'] or result['unreadable_statement']:
        kind = 'unreadable_or_refused'
    elif any(o.get('type') in ('function_call', 'custom_tool_call') for o in response.get('output', [])):
        kind = 'unexpected_tool_call'
    elif result['strict_match']:
        kind = 'exact_match'
    elif previous is not None and text.upper() == previous.upper():
        kind = 'previous_text_echo'
    elif result['correct']:
        kind = 'correct_content_format_mismatch'
    elif result['hex_candidate_count']:
        kind = 'incorrect_code'
    elif not text:
        kind = 'no_text_output'
    else:
        kind = 'no_hex_code'
    result['classification'] = kind
    return result


class OrdinaryImageVerifier(ToolImageVerifier):
    def private_write(self, name, raw):
        fd = os.open(os.path.join(self.evidence_dir, name),
                     os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'wb') as out:
            out.write(raw)
            out.flush()
            os.fsync(out.fileno())

    def capture(self, name, expected, response, previous=None):
        # Only this synthetic test's text is retained; never raw SSE, headers,
        # tool arguments, credentials, data URLs or signed image URLs.
        text = raw_text(response)
        self.private_write(name + '.json', json.dumps(
            {'expected': expected, 'reply_text': text}, ensure_ascii=False,
            indent=2).encode())
        diagnostic = diagnose(response, expected, previous)
        self.report['response_diagnostics'][-1].update(diagnostic)
        return diagnostic

    def run(self):
        self.evidence_dir = self.a.output + '.private'
        try:
            os.mkdir(self.evidence_dir, 0o700)
        except FileExistsError:
            raise Failure('evidence_directory_exists') from None
        phase('setup')
        self.setup()
        start = utc()
        self.report.update(started_at=start, private_evidence_preserved=True)
        previous = secrets.token_hex(4).upper()
        history = [{'role': 'user', 'content': [
            {'type': 'input_text', 'text': 'Reply with exactly ' + previous}]}]
        first = self.infer(history)
        first_diagnostic = self.capture('01-text', previous, first)
        if first_diagnostic['classification'] != 'exact_match':
            self.reconcile(start, [first])
            raise Failure('text_mismatch')
        history.extend(first['output'])
        nonce, img = self.image_part()
        self.private_write('02-ordinary-image.png', self.images[-1])
        self.private_write('02-ordinary-image-expected.txt', nonce.encode())
        self.report['image_sha256'] = hashlib.sha256(self.images[-1]).hexdigest()
        history.append({'role': 'user', 'content': [
            {'type': 'input_text', 'text': 'Read the eight hexadecimal characters in this image. Reply with only those characters.'}, img]})
        second = self.infer(history)
        diagnostics = self.capture('02-ordinary-image', nonce, second, previous)
        phase('verify_image_asset')
        self.check_assets()
        phase('reconcile_usage')
        self.reconcile(start, [first, second])
        phase('verify_exact_ocr')
        require(diagnostics['classification'] == 'exact_match', 'ordinary_image_ocr_mismatch')
        self.report['finished_at'] = utc()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ('account-id', 'group-id'):
        p.add_argument('--' + name, type=int, required=True)
    for name in ('model', 'output', 'container', 'db-container'):
        p.add_argument('--' + name, required=True)
    p.add_argument('--admin-base', default='http://127.0.0.1:18080')
    a = p.parse_args()
    require(urllib.parse.urlparse(a.admin_base).hostname in ('127.0.0.1', 'localhost', '::1'), 'admin_not_loopback')
    os.umask(0o077)
    fd = os.open(a.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    result = OrdinaryImageVerifier(a).execute()
    with os.fdopen(fd, 'w') as out:
        json.dump(result, out, indent=2)
        out.write(chr(10))
    phase('report_written_' + result['status'])
    return 0 if result['status'] == 'passed' else 1


if __name__ == '__main__':
    raise SystemExit(main())
