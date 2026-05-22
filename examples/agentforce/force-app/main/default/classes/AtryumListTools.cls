public with sharing class AtryumListTools {

    public class Request {
        @InvocableVariable(label='Server filter'
            description='Optional: limit results to a single Atryum server name (e.g. "github"). Leave blank for all servers.')
        public String server;

        @InvocableVariable(label='Keyword filter'
            description='Optional: case-insensitive substring match against server name, tool name, or tool description. Use this when you only know roughly what you are looking for (e.g. "slack", "issue", "send").')
        public String keyword;
    }

    public class Response {
        @InvocableVariable(description='JSON array of {server, toolName, description, inputSchema} entries.')
        public String catalogJson;

        @InvocableVariable(description='Human-readable summary suitable for the agent to reason over.')
        public String summary;
    }

    @InvocableMethod(label='Discover Atryum tools'
        description='List MCP tools available through Atryum, optionally filtered by server or keyword. Call this before AtryumDispatcher when you do not already know the correct server/toolName.'
        callout=true)
    public static List<Response> discover(List<Request> reqs) {
        List<Response> out = new List<Response>();
        for (Request r : reqs) {
            out.add(discoverOne(r));
        }
        return out;
    }

    private static Response discoverOne(Request r) {
        Response resp = new Response();
        List<Map<String,Object>> catalog = new List<Map<String,Object>>();

        // 1. Determine which servers to enumerate
        List<String> servers = new List<String>();
        if (String.isNotBlank(r.server)) {
            servers.add(r.server);
        } else {
            HttpRequest listReq = new HttpRequest();
            listReq.setEndpoint('callout:Atryum/api/v1/admin/servers?enabled=true&limit=200');
            listReq.setMethod('GET');
            HttpResponse lr = new Http().send(listReq);
            if (lr.getStatusCode() >= 300) {
                resp.summary = 'failed to list servers: ' + lr.getStatus();
                resp.catalogJson = '[]';
                return resp;
            }
            Map<String,Object> body = (Map<String,Object>) JSON.deserializeUntyped(lr.getBody());
            List<Object> items = (List<Object>) body.get('items');
            if (items == null) items = (List<Object>) body.get('servers');
            if (items != null) {
                for (Object o : items) {
                    Map<String,Object> s = (Map<String,Object>) o;
                    servers.add((String) s.get('name'));
                }
            }
        }

        // 2. Fetch tools for each server
        String kw = String.isBlank(r.keyword) ? null : r.keyword.toLowerCase();
        for (String name : servers) {
            HttpRequest tr = new HttpRequest();
            tr.setEndpoint('callout:Atryum/api/v1/admin/servers/' + EncodingUtil.urlEncode(name, 'UTF-8') + '/tools');
            tr.setMethod('GET');
            HttpResponse trr = new Http().send(tr);
            if (trr.getStatusCode() >= 300) continue;

            Map<String,Object> body = (Map<String,Object>) JSON.deserializeUntyped(trr.getBody());
            List<Object> tools = (List<Object>) body.get('items');
            if (tools == null) tools = (List<Object>) body.get('tools');
            if (tools == null) continue;

            for (Object o : tools) {
                Map<String,Object> t = (Map<String,Object>) o;
                String toolName = (String) t.get('name');
                String descr = (String) t.get('description');
                if (kw != null) {
                    String hay = (name == null ? '' : name.toLowerCase())
                               + ' ' + (toolName == null ? '' : toolName.toLowerCase())
                               + ' ' + (descr == null ? '' : descr.toLowerCase());
                    if (!hay.contains(kw)) continue;
                }
                catalog.add(new Map<String,Object>{
                    'server'      => name,
                    'toolName'    => toolName,
                    'description' => descr,
                    'inputSchema' => t.get('inputSchema')
                });
            }
        }

        // 3. Build summary
        resp.catalogJson = JSON.serialize(catalog);
        List<String> lines = new List<String>{ 'Available tools (' + catalog.size() + '):' };
        for (Map<String,Object> entry : catalog) {
            lines.add('- ' + entry.get('server') + '.' + entry.get('toolName')
                + ': ' + (entry.get('description') == null ? '' : entry.get('description')));
        }
        resp.summary = String.join(lines, '\n');
        return resp;
    }
}
