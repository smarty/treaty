const { one } = require("../src/util/a");
const util = require("../src/util/b");

function sum(a, b) {
  return a + b + one() + util.two();
}

const fmt = (value) => String(value);

exports.version = "1.0";

module.exports = { sum, format: fmt, total(items) { return items.reduce(sum, 0); } };
