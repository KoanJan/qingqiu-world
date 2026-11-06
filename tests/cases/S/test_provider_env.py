"""Verify the S-track provider prerequisite without using provider credentials."""

import unittest

from provider_env import REQUIRED_PROVIDER_ENV, missing_provider_env


class ProviderEnvironmentTests(unittest.TestCase):
    def test_every_provider_input_is_required(self):
        configured = {name: "present" for name in REQUIRED_PROVIDER_ENV}
        self.assertEqual(len(configured), 6)
        self.assertEqual(missing_provider_env(configured), [])
        for name in REQUIRED_PROVIDER_ENV:
            with self.subTest(name=name):
                omitted = {key: value for key, value in configured.items() if key != name}
                self.assertEqual(missing_provider_env(omitted), [name])
                self.assertEqual(missing_provider_env({**configured, name: "  "}), [name])


if __name__ == "__main__":
    unittest.main()
