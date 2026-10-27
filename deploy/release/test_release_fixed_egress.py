import unittest
from release_fixed_egress import verify_fixed_egress_snapshot, management_counts, rehearsal_environment

class FixedEgressReleaseTests(unittest.TestCase):
    def test_requires_default_off_and_no_unexpected_resources(self):
        valid = {'config': {'enabled': False}, 'pools': [], 'providers': [], 'resources': [], 'bindings': []}
        verify_fixed_egress_snapshot(valid)
        for field in ['pools', 'providers', 'resources', 'bindings']:
            bad = {**valid, field: [{'id': 1}]}
            with self.assertRaises(ValueError): verify_fixed_egress_snapshot(bad)
        with self.assertRaises(ValueError): verify_fixed_egress_snapshot({**valid, 'config': {'enabled': True}})
        with self.assertRaises(ValueError): verify_fixed_egress_snapshot({})

    def test_management_counts_excludes_live_usage_and_tracks_retained_records(self):
        names = management_counts()
        self.assertIn('accounts', names)
        self.assertIn('proxies', names)
        self.assertIn('api_keys', names)
        self.assertIn('account_group_members', names)
        self.assertNotIn('usage_logs', names)

class RehearsalTests(unittest.TestCase):
    def test_preserves_values_and_overrides_only_scratch_database_and_mode(self):
        self.assertEqual(rehearsal_environment({'DATABASE_NAME':'live','ADMIN_SECRET':'abc=def'}, 'scratch'), 'ADMIN_SECRET=abc=def\nCODEX_MIGRATE_ONLY=1\nDATABASE_NAME=scratch\n')
    def test_rejects_multiline_values(self):
        with self.assertRaises(ValueError): rehearsal_environment({'ADMIN_SECRET':'a\nb'}, 'scratch')
