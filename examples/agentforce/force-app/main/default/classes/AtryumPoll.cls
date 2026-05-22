public with sharing class AtryumPoll {

    public class Request {
        @InvocableVariable(required=true) public String invocationId;
    }

    public class Response {
        @InvocableVariable public String status;
        @InvocableVariable public String resultJson;
        @InvocableVariable public String denyReason;
    }

    @InvocableMethod(label='Re-check Atryum invocation'
        description='Returns the current status of a previously submitted Atryum invocation.'
        callout=true)
    public static List<Response> poll(List<Request> reqs) {
        List<Response> out = new List<Response>();
        for (Request r : reqs) {
            HttpRequest req = new HttpRequest();
            req.setEndpoint('callout:Atryum/api/v1/external/invocations/' + r.invocationId);
            req.setMethod('GET');
            HttpResponse pr = new Http().send(req);
            Map<String,Object> state = (Map<String,Object>) JSON.deserializeUntyped(pr.getBody());
            Response resp = new Response();
            resp.status = (String) state.get('status');
            resp.resultJson = JSON.serialize(state.get('result'));
            resp.denyReason = (String) state.get('deny_reason');
            out.add(resp);
        }
        return out;
    }
}
