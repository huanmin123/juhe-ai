const net = require('node:net')

net.createServer().listen(Number(process.argv[2]), '127.0.0.1')
