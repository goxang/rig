import os
import sys
import unittest
from xml.etree import ElementTree as ET

from app import greet


class GreetTest(unittest.TestCase):
    def test_greet(self):
        self.assertEqual(greet("rig"), "hello, rig")

    def test_greet_needs_a_name(self):
        with self.assertRaises(ValueError):
            greet("")


class JUnitResult(unittest.TextTestResult):
    """Also records each test for $RIG_JUNIT, so rig shows them one by one."""

    cases = []

    def addSuccess(self, test):
        super().addSuccess(test)
        self.cases.append((test.id(), None))

    def addFailure(self, test, err):
        super().addFailure(test, err)
        self.cases.append((test.id(), self._exc_info_to_string(err, test)))

    addError = addFailure


if __name__ == "__main__":
    result = unittest.main(exit=False, verbosity=2, testRunner=unittest.TextTestRunner(resultclass=JUnitResult)).result
    if path := os.environ.get("RIG_JUNIT"):
        suite = ET.Element("testsuite", name="unit", tests=str(len(JUnitResult.cases)))
        for name, failure in JUnitResult.cases:
            case = ET.SubElement(suite, "testcase", classname=name.rsplit(".", 1)[0], name=name.rsplit(".", 1)[1])
            if failure:
                ET.SubElement(case, "failure").text = failure
        ET.ElementTree(suite).write(path)
    sys.exit(not result.wasSuccessful())
