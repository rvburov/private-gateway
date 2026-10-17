#!/usr/bin/env python3
"""Content tags change for exactly the images whose Dockerfile or copied files change."""

import importlib.util
from pathlib import Path
import shutil
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("image_tags", ROOT / "deploy/docker/image-tags.py")
image_tags = importlib.util.module_from_spec(spec)
spec.loader.exec_module(image_tags)


class ImageTagTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        # The layout of an extracted release, with the real Dockerfiles.
        self.release = Path(temporary.name)
        for name in ("Dockerfile.api", "Dockerfile.xray", "Dockerfile.browser"):
            self.write(f"docker/{name}", (ROOT / "deploy/docker" / name).read_text())
        for name in ("bin/gateway-api", "bin/gatewayctl", "bin/xray", "docker/wait-xray.py",
                     "docker/xray-worker.py", "docker/browser-worker.py", "browser/start.sh",
                     "browser/healthcheck.sh", "browser/smoke.py", "web/index.html", "web/assets/app.js"):
            self.write(name, f"content of {name}\n")

    def write(self, name, content):
        path = self.release / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)

    def tags(self):
        return {variable: image_tags.content_tag(self.release, name) for variable, name in image_tags.IMAGES}

    def changed_after(self, change):
        before = self.tags()
        change()
        after = self.tags()
        return {variable for variable in before if before[variable] != after[variable]}

    def test_tags_are_stable_and_distinct(self):
        tags = self.tags()
        self.assertEqual(tags, self.tags())
        self.assertEqual(len(set(tags.values())), 3)
        for tag in tags.values():
            self.assertRegex(tag, r"^[0-9a-f]{32}$")

    def test_panel_change_retags_only_the_api(self):
        self.assertEqual(self.changed_after(lambda: self.write("web/assets/new.js", "new\n")), {"PG_API_IMAGE"})
        self.assertEqual(self.changed_after(lambda: self.write("web/index.html", "changed\n")), {"PG_API_IMAGE"})

    def test_xray_binary_retags_api_and_xray(self):
        self.assertEqual(self.changed_after(lambda: self.write("bin/xray", "new Xray\n")),
                         {"PG_API_IMAGE", "PG_XRAY_IMAGE"})

    def test_worker_and_dockerfile_changes(self):
        self.assertEqual(self.changed_after(lambda: self.write("docker/xray-worker.py", "new\n")),
                         {"PG_XRAY_IMAGE"})
        self.assertEqual(self.changed_after(lambda: self.write("browser/start.sh", "new\n")),
                         {"PG_BROWSER_IMAGE"})
        dockerfile = self.release / "docker/Dockerfile.browser"
        self.assertEqual(self.changed_after(lambda: self.write(
            "docker/Dockerfile.browser", dockerfile.read_text() + "LABEL test=1\n")), {"PG_BROWSER_IMAGE"})

    def test_files_outside_copy_sources_do_not_matter(self):
        self.assertEqual(self.changed_after(lambda: self.write("docker/update.sh", "new\n")), set())
        self.assertEqual(self.changed_after(lambda: self.write("docker/compose.yml", "new\n")), set())

    def test_refuses_missing_or_escaping_sources(self):
        (self.release / "docker/wait-xray.py").unlink()
        with self.assertRaises(SystemExit):
            self.tags()
        self.write("docker/wait-xray.py", "restored\n")
        self.write("docker/Dockerfile.xray", "FROM scratch\nCOPY ../secret /secret\n")
        with self.assertRaises(SystemExit):
            self.tags()
        self.write("docker/Dockerfile.xray", "FROM scratch\nCOPY --from=builder /x /x\n")
        with self.assertRaises(SystemExit):
            self.tags()
        self.write("docker/Dockerfile.xray", "FROM scratch\nADD https://example.com/x /x\n")
        with self.assertRaises(SystemExit):
            self.tags()

    def test_refuses_symlinked_sources(self):
        target = self.release / "web"
        shutil.move(target, self.release / "elsewhere")
        target.symlink_to(self.release / "elsewhere", target_is_directory=True)
        with self.assertRaises(SystemExit):
            self.tags()


if __name__ == "__main__":
    unittest.main()
