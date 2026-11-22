using System.Collections.Immutable;
using System.Buffers.Binary;
using System.Security.Cryptography;
using System.Text.Json;
using Serve100.Game;
using Serve100.Host;
using Serve100.Protocol;

if(args.Length!=1)return 2;
if(args[0]=="--recipient-codepage")
{
    byte[] request=[2,0,0,0,0,0,0],words=new byte[126*190*2];
    int index=0;
    for(int lead=0x81;lead<=0xfe;lead++)
    for(int trail=0x40;trail<=0xfe;trail++)
    {
        if(trail==0x7f)continue;
        request[4]=(byte)lead;request[5]=(byte)trail;
        var query=LocalPlayerMailPackets.ReadRecipient(request,LocalMailBodyForm.NativeFields);
        var ack=LocalPlayerMailPackets.RecipientAccepted(query.Name,6,1,0,0);
        if(query.Name.Length!=1 || !ack.AsSpan(5,2).SequenceEqual(request.AsSpan(4,2)))throw new InvalidDataException("CP936 round-trip");
        BinaryPrimitives.WriteUInt16LittleEndian(words.AsSpan(index),query.Name[0]);index+=2;
    }
    Console.WriteLine($"{Environment.Version} {index/2} {Convert.ToHexString(SHA256.HashData(words))}");
    return 0;
}
var mailbox=LocalMailboxState.Empty;
var template=new LocalItemTemplate(3033,LocalItemKind.Stackable,121,176,ushort.MaxValue,10000,0,1,ushort.MaxValue,new string('A',64));
var own=new LocalAssetGrant(template,3,new(0,0));
for(int i=0;i<23;i++)
{
    var draft=new LocalSystemMailDraft("赛丽亚","系统邮件","第"+i+"封，角色邮箱已收到。",i==0?123u:i==1?22u:0u,i==0?[own]:[]);
    var issued=LocalMailboxRules.Deliver(1,mailbox,"wire-fixture-"+i,draft,1700000000u+(uint)i);
    mailbox=issued.Next;
    if(i==2)mailbox=LocalMailboxRules.ChangeVisible(mailbox,[issued.MailId],3);
}
var gameplay=(LocalGameplayProfile.Empty with{Mailbox=mailbox}).MaterializePendingAssets(()=>Guid.Parse("6b79795e-28d1-4fc7-bd30-72ee32b7abaa"));
var c=new LocalCharacter(1,0,"邮件样本",0,1,1,1,new(ImmutableDictionary<string,long>.Empty),38,1,450,234,5,3,""){Gameplay=gameplay};
var account=LocalAccountSnapshot.Empty(Guid.Empty) with{Characters=[c],Selected=c};
var projected=LocalCommerceModule.MailboxProjection(account);
using var input=File.OpenRead(typeof(LocalCommerceModule).Assembly.Location);
var result=new{Origin="Original LocalMailboxRules.Deliver, ChangeVisible, MaterializePendingAssets and Host.MailboxProjection",HostSha256=Convert.ToHexString(SHA256.HashData(input)),gameplay.Mailbox,Packets=projected.Select(p=>new{p.Opcode,p.Body})};
File.WriteAllText(args[0],JsonSerializer.Serialize(result));
return 0;
