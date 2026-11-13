// Runs fit.js with a stand-in window and prints the results of its pure parts as JSON.
const fs = require('fs');
global.window = {};
global.document = {};
eval(fs.readFileSync(process.argv[2], 'utf8'));
const w = global.window;
console.log(JSON.stringify({
  big: w.weftFitSize(4032, 3024, 1600),
  tall: w.weftFitSize(3024, 4032, 1600),
  small: w.weftFitSize(800, 600, 1600),
  thin: w.weftFitSize(10000, 1, 1600),
  tries: w.weftFitTries(4032, 1600),
  triesSmall: w.weftFitTries(900, 1600),
}));
