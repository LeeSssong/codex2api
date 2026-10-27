import unittest
from collaboration_guard import check


class CollaborationGuardTests(unittest.TestCase):
    def test_shared_checkout_rejected_on_any_branch(self):
        for event in ['pre-commit', 'pre-push']:
            with self.assertRaises(ValueError):
                check('/Users/gongtengxinwen/Documents/codex2api', 'grok/work', event)

    def test_task_worktree_allowed(self):
        check('/tmp/isolated-task', 'codex/quota', 'pre-commit')

    def test_main_commits_rejected_but_integrated_main_push_allowed(self):
        with self.assertRaises(ValueError):
            check('/tmp/integration', 'main', 'pre-commit')
        check('/tmp/release', 'main', 'pre-push', [('refs/heads/main', 'refs/heads/main')])

    def test_feature_cannot_push_over_main(self):
        with self.assertRaises(ValueError):
            check('/tmp/task', 'codex/quota', 'pre-push', [('refs/heads/codex/quota', 'refs/heads/main')])


if __name__ == '__main__':
    unittest.main()
