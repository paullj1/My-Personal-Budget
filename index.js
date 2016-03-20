var http = require('http')

http.createServer(function (request, response) {
  response.writeHead(200, {"Content-Type": "text/plain"})
  response.end("My Personal Budget\n")
}).listen(process.env.PORT)
