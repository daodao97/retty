import importlib.util
import json
from pathlib import Path
import socket
import tempfile
import threading
import unittest


spec = importlib.util.spec_from_file_location("performance_api", Path(__file__).with_name("performance-api.py"))
api = importlib.util.module_from_spec(spec)
spec.loader.exec_module(api)


class ProbeTests(unittest.TestCase):
    def server(self, requests, connections):
        temporary = tempfile.TemporaryDirectory(prefix="retty-api-probe-", dir="/tmp")
        self.addCleanup(temporary.cleanup)
        path = Path(temporary.name) / "fixture.sock"
        listener = socket.socket(socket.AF_UNIX)
        listener.bind(str(path))
        listener.listen()
        listener.settimeout(3)
        self.addCleanup(listener.close)
        seen, errors = [], []
        def serve():
            try:
                for _ in range(connections):
                    conn, _ = listener.accept()
                    conn.settimeout(3)
                    with conn, conn.makefile("rb") as stream:
                        for _ in range(requests // connections):
                            request = json.loads(stream.readline())
                            seen.append(request)
                            response = {"id": request["id"], "etag": "fixture"}
                            if request.get("etag") != "fixture":
                                response["data"] = "中文🙂完整响应"
                            wire = json.dumps(response, ensure_ascii=False).encode() + b"\n"
                            for pos in range(0, len(wire), 3):
                                conn.sendall(wire[pos:pos + 3])
            except BaseException as error:
                errors.append(error)
        thread = threading.Thread(target=serve)
        thread.start()
        def check():
            thread.join(timeout=5)
            self.assertFalse(thread.is_alive(), "owned probe server did not stop")
            self.assertEqual(errors, [])
        self.addCleanup(check)
        return path, seen

    def test_cached_connection_reuses_etag_and_excludes_warmup(self):
        path, seen = self.server(5, 1)
        result = api.measure(path, {"op": "list"}, count=3, warmup=2, cached=True)
        self.assertEqual(len(result["samples"]), 3)
        self.assertEqual(result["p99"], max(result["samples"]))
        self.assertEqual(result["connection"], "persistent")
        self.assertNotIn("etag", seen[0])
        self.assertTrue(all(r["etag"] == "fixture" for r in seen[1:]))
        self.assertEqual([r["id"] for r in seen], [1, 2, 3, 4, 5])

    def test_new_connection_probe_really_opens_each_connection(self):
        path, seen = self.server(4, 4)
        result = api.measure(path, {"op": "hello"}, count=3, warmup=1, fresh=True)
        self.assertEqual(len(result["samples"]), 3)
        self.assertEqual(len(seen), 4)
        self.assertTrue(all(r["id"] == 1 for r in seen))
        self.assertGreater(result["mean_wire_bytes"], 0)


if __name__ == "__main__":
    unittest.main()
