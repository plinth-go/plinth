const sseLog = document.querySelector('#sse-log');
const wsLog = document.querySelector('#ws-log');

document.querySelector('#sse-start').addEventListener('click', () => {
  sseLog.textContent = 'connecting…';
  const source = new EventSource('/demo/events');
  source.addEventListener('tick', (event) => {
    sseLog.textContent = `${new Date().toLocaleTimeString()}\n${event.data}`;
  });
  source.onerror = () => {
    sseLog.textContent = 'stream disconnected';
    source.close();
  };
});

document.querySelector('#ws-start').addEventListener('click', () => {
  const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
  const socket = new WebSocket(`${scheme}://${location.host}/demo/ws`);
  wsLog.textContent = 'connecting…';
  socket.onopen = () => {
    socket.send('ping from webui');
  };
  socket.onmessage = (event) => {
    wsLog.textContent = `echo: ${event.data}`;
  };
  socket.onclose = () => {
    wsLog.textContent += '\nclosed';
  };
  socket.onerror = () => {
    wsLog.textContent = 'websocket error';
  };
});
