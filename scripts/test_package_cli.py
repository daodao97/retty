import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location("package_cli", Path(__file__).with_name("package-cli.py"))
package_cli = importlib.util.module_from_spec(spec)
spec.loader.exec_module(package_cli)


class CLIDependencyTests(unittest.TestCase):
    def test_headless_apns_and_screen_are_allowed(self):
        package_cli.validate_dependencies("net/http\ngithub.com/egoist/mygo/push/apns\nretty/internal/terminal/screen\n")

    def test_gui_and_unreviewed_framework_packages_are_rejected(self):
        for name in ["github.com/egoist/mygo", "github.com/egoist/mygo/ui",
                     "github.com/egoist/mygo/internal/darwin", "github.com/egoist/mygo/push/apns/native",
                     "retty/internal/terminal"]:
            with self.subTest(package=name):
                with self.assertRaisesRegex(RuntimeError, "graphics/UI runtime") as error:
                    package_cli.validate_dependencies("net/http\ngithub.com/egoist/mygo/push/apns\n" + name)
                self.assertIn(name, str(error.exception))


if __name__ == "__main__":
    unittest.main()
