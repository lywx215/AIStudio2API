"""Focused acceptance checks for the standalone operator deployment helper."""
import contextlib
import io
import json
import pathlib
import runpy
import unittest

MODULE = runpy.run_path(str(pathlib.Path(__file__).with_name('vps_deploy.py')), run_name='vps_deploy_test')


class DeploymentChecks(unittest.TestCase):
    def test_remote_stage_does_not_collide_with_local_event(self):
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            MODULE['emit']('deployment_progress', stage='build_image', job_active=True)
        record = json.loads(output.getvalue())
        self.assertEqual(record['event'], 'deployment_progress')
        self.assertEqual(record['stage'], 'build_image')

    def test_all_uploaded_remote_programs_compile(self):
        for name in ['PREFLIGHT', 'JOB', 'STATUS', 'VERIFY', 'HTTPS']:
            with self.subTest(program=name):
                compile(MODULE[name], name, 'exec')


if __name__ == '__main__':
    unittest.main()
