import unittest
from unittest import mock

from llmrx_client.enroll import EnrollError, enroll
from tests.helpers import MockServerAPI


class TestEnroll(unittest.TestCase):
    def test_success_returns_credential_pair(self):
        with MockServerAPI() as api:
            doc = enroll(api.url, "good-code")
        self.assertEqual(doc["client_id"], "ec_abcd1234567890ef")
        self.assertEqual(doc["client_secret"], "ecs_new_secret")
        self.assertEqual(doc["name"], "py-edge")

    def test_request_shape(self):
        with MockServerAPI() as api:
            enroll(api.url, "good-code", name="ignored")
            sent = api.enroll_requests[0]
        self.assertEqual(sent["enroll_token"], "good-code")
        # The server ignores client_name (the name comes from the code);
        # the Go client sends whatever -enroll-name was passed.
        self.assertIn("client_name", sent)

    def test_bad_code_is_401_with_message(self):
        with MockServerAPI() as api:
            with self.assertRaises(EnrollError) as ctx:
                enroll(api.url, "bad-code")
        msg = str(ctx.exception)
        self.assertIn("enroll rejected (401 invalid_enroll_token)", msg)
        self.assertIn("invalid, expired or already used", msg)

    def test_empty_secret_rejected(self):
        with MockServerAPI() as api:
            # Point the mock at a 201 without a secret by hand-rolling:
            import json as j
            import urllib.request

            req = urllib.request.Request(
                api.url + "/client/v1/enroll",
                data=j.dumps({"enroll_token": "good-code",
                              "client_name": ""}).encode(),
                method="POST",
                headers={"Content-Type": "application/json"})
            # (the mock always returns a secret for good-code, so simulate
            # the empty-secret branch directly at the parsing level)
            from llmrx_client import enroll as mod

            class FakeResp:
                status = 201

                def __enter__(self):
                    return self

                def __exit__(self, *a):
                    return False

                def read(self, n=-1):
                    return j.dumps({"client_id": "ec_x",
                                    "client_secret": "",
                                    "name": "n"}).encode()

            with mock.patch.object(mod.urllib.request, "urlopen",
                                   lambda r, timeout: FakeResp()):
                with self.assertRaises(EnrollError) as ctx:
                    mod.enroll(api.url, "good-code")
            self.assertIn("empty secret", str(ctx.exception))

    def test_unreachable_server(self):
        with self.assertRaises(EnrollError):
            enroll("http://127.0.0.1:9", "x")


if __name__ == "__main__":
    unittest.main()
