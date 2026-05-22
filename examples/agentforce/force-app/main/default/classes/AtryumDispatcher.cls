public with sharing class AtryumDispatcher {

    public class Request {
        @InvocableVariable(required=true label='Tool name'
            description='Upstream MCP tool name (e.g. create_issue).')
        public String toolName;

        @InvocableVariable(required=true label='Server'
            description='Atryum-configured upstream server name (e.g. github).')
        public String server;

        @InvocableVariable(required=true label='Arguments JSON'
            description='JSON-encoded arguments object for the tool call.')
        public String argumentsJson;
    }

    public class Response {
        @InvocableVariable(description='ok | denied | error | tool_error')
        public String status;

        @InvocableVariable(description='Text content returned by the tool (concatenated MCP content blocks).')
        public String resultText;

        @InvocableVariable(description='Raw JSON of the full MCP result for the agent to inspect when needed.')
        public String resultJson;

        @InvocableVariable(description='Error or denial message when status != ok.')
        public String errorMessage;
    }

    @InvocableMethod(label='Call Atryum-mediated MCP tool'
        description='Invoke a tool on an Atryum-registered MCP server. Atryum gates approval, audits the call, and proxies to the upstream.'
        callout=true)
    public static List<Response> dispatch(List<Request> requests) {
        List<Response> out = new List<Response>();
        for (Request r : requests) {
            out.add(callOne(r));
        }
        return out;
    }

    private static Response callOne(Request r) {
        Response resp = new Response();

        Map<String,Object> args;
        try {
            args = (Map<String,Object>) JSON.deserializeUntyped(
                String.isBlank(r.argumentsJson) ? '{}' : r.argumentsJson);
        } catch (Exception e) {
            resp.status = 'error';
            resp.errorMessage = 'argumentsJson is not valid JSON: ' + e.getMessage();
            return resp;
        }

        Map<String,Object> envelope = new Map<String,Object>{
            'jsonrpc' => '2.0',
            'id'      => 1,
            'method'  => 'tools/call',
            'params'  => new Map<String,Object>{
                'name'      => r.toolName,
                'arguments' => args
            }
        };

        HttpRequest req = new HttpRequest();
        req.setEndpoint('callout:Atryum/mcp/' + EncodingUtil.urlEncode(r.server, 'UTF-8'));
        req.setMethod('POST');
        req.setHeader('Content-Type', 'application/json');
        req.setHeader('Accept', 'application/json');
        req.setTimeout(120000);
        req.setBody(JSON.serialize(envelope));

        HttpResponse hr;
        try {
            hr = new Http().send(req);
        } catch (CalloutException ce) {
            resp.status = 'error';
            resp.errorMessage = 'callout failed: ' + ce.getMessage();
            return resp;
        }

        Integer code = hr.getStatusCode();
        String body = hr.getBody();

        if (code == 403) {
            resp.status = 'denied';
            resp.errorMessage = body;
            return resp;
        }
        if (code >= 300) {
            resp.status = 'error';
            resp.errorMessage = 'HTTP ' + code + ': ' + body;
            return resp;
        }

        Map<String,Object> decoded;
        try {
            decoded = (Map<String,Object>) JSON.deserializeUntyped(body);
        } catch (Exception e) {
            resp.status = 'error';
            resp.errorMessage = 'non-JSON response from atryum: ' + body;
            return resp;
        }

        // JSON-RPC transport-level error
        if (decoded.get('error') != null) {
            Map<String,Object> err = (Map<String,Object>) decoded.get('error');
            resp.status = 'error';
            resp.errorMessage = String.valueOf(err.get('message'));
            return resp;
        }

        Map<String,Object> result = (Map<String,Object>) decoded.get('result');
        if (result == null) {
            resp.status = 'error';
            resp.errorMessage = 'missing result in response: ' + body;
            return resp;
        }

        resp.resultJson = JSON.serialize(result);
        resp.resultText = flattenContent((List<Object>) result.get('content'));

        Boolean isError = result.get('isError') == true;
        resp.status = isError ? 'tool_error' : 'ok';
        if (isError && String.isBlank(resp.errorMessage)) {
            resp.errorMessage = resp.resultText;
        }
        return resp;
    }

    private static String flattenContent(List<Object> content) {
        if (content == null) return '';
        List<String> parts = new List<String>();
        for (Object o : content) {
            Map<String,Object> block = (Map<String,Object>) o;
            String t = (String) block.get('type');
            if (t == 'text' && block.get('text') != null) {
                parts.add(String.valueOf(block.get('text')));
            }
        }
        return String.join(parts, '\n');
    }
}
