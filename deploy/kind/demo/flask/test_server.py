import unittest

from server import app


class FlaskDemoTest(unittest.TestCase):
    def setUp(self):
        self.client = app.test_client()

    def test_health(self):
        resp = self.client.get("/health")
        self.assertEqual(resp.status_code, 200)
        self.assertEqual(resp.get_data(as_text=True), "ok")

    def test_hello(self):
        resp = self.client.get("/api/hello")
        self.assertEqual(resp.status_code, 200)
        body = resp.get_json()
        self.assertEqual(body["service"], "flask-demo")
        self.assertEqual(body["message"], "hello from flask")

    def test_slow(self):
        resp = self.client.get("/api/slow")
        self.assertEqual(resp.status_code, 200)
        self.assertEqual(resp.get_json(), {"service": "flask-demo", "slow": True})

    def test_error(self):
        resp = self.client.get("/api/error")
        self.assertEqual(resp.status_code, 500)
        self.assertEqual(resp.get_json(), {"service": "flask-demo", "error": "simulated"})


if __name__ == "__main__":
    unittest.main()
