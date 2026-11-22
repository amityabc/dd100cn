using System.Collections;
using System.Reflection;
using Serve100.Game;
using Serve100.Infrastructure.Resources;

internal static class CommerceExport
{
    private static object Field(object owner, string name) =>
        owner.GetType().GetField(name, BindingFlags.Instance | BindingFlags.NonPublic)?.GetValue(owner)
        ?? throw new InvalidDataException($"Missing prepared commerce field {name}");

    private static object Property(object owner, string name) =>
        owner.GetType().GetProperty(name)?.GetValue(owner)
        ?? throw new InvalidDataException($"Missing prepared commerce field {name}");

    private static IEnumerable<(object Key,object Value)> Pairs(object map) =>
        ((IEnumerable)map).Cast<object>().Select(r => (Property(r,"Key"),Property(r,"Value")));

    private static object Permission(object p) => new {
        Level=Property(p,"Level"), All=Property(p,"All"), Any=Property(p,"Any"),
        Except=Property(p,"Except"), Started=Property(p,"Started"), Blocked=Property(p,"Blocked")
    };

    internal static object Read(LocalCommerceCatalog commerce,LocalPvfItemTemplates items,IEnumerable<uint> ids,PvfResourceProvider resources)
    {
        commerce.PrepareGiftBoxes(ids);
        items.Prepare([8008]);
        commerce.PrepareLottery(ids.Concat(commerce.GiftTemplateIds),items);
        var lottery=(LocalPvfLotteryCatalog)Field(commerce,"_lottery");
        var keyedBoxes=new LocalPvfRandomBoxCatalog(resources);
        var keyedRoots=new uint[]{490708133}.Concat(ids).Distinct().ToArray();
        keyedBoxes.Prepare(keyedRoots);
        items.RegisterGiftTemplates(keyedBoxes.Templates);
        var luckyBoxes=new LocalPvfAncientHeroLuckyBoxCatalog(resources);
        var luckyRoots=new uint[]{490708135}.Concat(ids).Distinct().ToArray();
        luckyBoxes.Prepare(luckyRoots);
        // Lucky-box grants carry acquisition metadata owned by that reward
        // table. Keep their templates in LuckyBoxes instead of replacing an
        // ordinary shop/quest template or the keyed box's source/reward.
        var cargoGift=new LocalPvfCargoStageGiftCatalog(resources);
        cargoGift.Prepare();
        var coupons=new LocalPvfContainerCouponCatalog(resources);
        coupons.Prepare();
        var gifts = (LocalPvfGiftBoxCatalog)Field(commerce, "_giftBoxes");
        var templates = ids.Select(id => gifts.Templates.GetValueOrDefault(id) ?? items.Get(id))
            .OfType<LocalItemTemplate>().ToDictionary(t => t.TemplateId);
        var boxWeights=new LocalPlayerMailPolicy(resources,items);
        var boxIds=lottery.Templates.Concat(keyedBoxes.Templates).Concat(luckyBoxes.Templates)
            .Select(t=>t.TemplateId).Distinct().ToArray();
        boxWeights.Prepare(boxIds);
        var boxWeightMap=new Dictionary<uint,long>();
        foreach(var id in boxIds)
        {
            try { boxWeightMap.Add(id,boxWeights.ItemWeight(id)); }
            catch(InvalidDataException) { /* Unsupported inventory weights cannot authorize a grant. */ }
            catch(FileNotFoundException) { }
        }
        var quality=ReadQuality(commerce,items,resources,ids);
        var craft=ReadCraft(commerce,items,resources,ids,boxWeights,boxWeightMap);
        var apcs=ReadGmApcs(resources);
        var fate=new LocalFateCatalog(resources);
        fate.Prepare();
        var fateExchange=new LocalFateExchangeCatalog(resources,fate.Definition);
        fateExchange.Prepare();
        var fateBoxes=new LocalFateRewardBoxCatalog(resources,fateExchange.Definition);
        fateBoxes.Prepare();
        if(fateExchange.Definition is {} exchange)
        {
            foreach(var offer in exchange.Offers.Values)
            {
                foreach(var item in new[]{offer.Product,offer.Material})
                {
                    boxWeights.Prepare([item.TemplateId]);
                    boxWeightMap.TryAdd(item.TemplateId,boxWeights.ItemWeight(item.TemplateId));
                }
            }
        }
        if(fate.Definition is {} activeFate)
        {
            foreach(var item in activeFate.StageBoxes.Values.SelectMany(row=>new[]{row.Source,row.Reward})
                .Concat(fateBoxes.Definition?.Boxes.Values.SelectMany(row=>new[]{row.Source,row.Reward})??[]).DistinctBy(t=>t.TemplateId))
            {
                boxWeights.Prepare([item.TemplateId]);
                boxWeightMap.TryAdd(item.TemplateId,boxWeights.ItemWeight(item.TemplateId));
            }
        }
        var town = (Dictionary<(uint Npc,uint Shop,byte Job,uint Item),LocalNpcOffer>)Field(commerce,"_npc");
        var dungeon = (Dictionary<(uint Npc,uint Shop,byte Job,uint Item),LocalNpcOffer>)Field(commerce,"_dungeonNpc");
        foreach (var id in ids) commerce.PreheatItem(id);
        var sale = (Dictionary<uint,uint>)Field(commerce,"_sale");
        var peers = (Dictionary<(uint Npc,uint Shop),HashSet<uint>>)Field(commerce,"_townShops");
        var cash = (Dictionary<uint,LocalCashOffer>)Field(commerce,"_cash");
        var petRoots=cash.Values.Where(o=>o.PetTemplate is not null).Select(o=>o.TemplateId)
            .Concat(gifts.SpecialProducts.Where(p=>p.Template.Kind==LocalSpecialItemKind.Pet).Select(p=>p.Template.TemplateId)).Distinct().ToArray();
        commerce.PetHatches.Prepare(petRoots);
        commerce.PetRenames.Prepare(petRoots);
        commerce.PetCare.Prepare();
        var hatch=petRoots.Select(commerce.PetHatches.Get).OfType<LocalPetHatchDefinition>().ToDictionary(p=>p.Egg.TemplateId);
        var petNames=petRoots.Concat(hatch.Values.Select(h=>h.Pet.TemplateId)).Distinct().Select(commerce.PetRenames.GetPet)
            .OfType<LocalPetRenameTarget>().ToDictionary(p=>p.TemplateId);
        var visibility = Field(commerce,"_visibility");
        var scene = Field(commerce,"_townScenes");
        var areas = Pairs(Field(scene,"_areas")).Select(p=>new {
            Town=Property(p.Key,"Town"), Area=Property(p.Key,"Area"),
            Path=Property(p.Value,"Path"), Phases=Property(p.Value,"Phases"),
            Permission=Permission(Property(p.Value,"Permission"))
        }).ToArray();
        var phases = Pairs(Field(scene,"_phases")).ToDictionary(p=>(ushort)p.Key,p=>p.Value);
        var maps = Pairs(Field(scene,"_maps")).ToDictionary(p=>((string)p.Key).ToLowerInvariant(),p=>p.Value);
        var visibilityRows = Pairs(Field(visibility,"_rules")).ToDictionary(p=>(uint)p.Key,p=>p.Value);
        var avatars=new LocalCashAvatarCatalog(resources);
        avatars.Prepare();
        avatars.PrepareGiftBoxes(ids.Concat(commerce.GiftTemplateIds));
        var avatarOffers=Pairs(Field(avatars,"_products")).ToDictionary(p=>(uint)p.Key,p=>p.Value);
        var avatarBoxes=Pairs(Field(avatars,"_giftBoxes")).Select(p=>(LocalAvatarBoxDefinition)p.Value)
            .ToDictionary(box=>box.Source.TemplateId);
        var sortSources=templates.Values.Select(t=>new LocalInventorySortSource(t.TemplateId,t.Kind!=LocalItemKind.Stackable,t.ResourceHash))
            .Concat(avatarOffers.Values.Select(v=>(LocalSpecialTemplate)Property(v,"Template"))
                .Select(t=>new LocalInventorySortSource(t.TemplateId,true,t.ResourceHash))).Distinct().ToArray();
        var sort=new LocalInventorySortCatalog(resources);
        sort.Prepare(sortSources);
        var clones=new LocalAvatarCloneCatalog(resources);
        var cloneIds=avatarOffers.Values.Select(v=>(LocalSpecialTemplate)Property(v,"Template"))
            .Concat(gifts.SpecialProducts.Where(p=>p.Template.Kind==LocalSpecialItemKind.Avatar).Select(p=>p.Template))
            .Concat(avatarBoxes.Values.SelectMany(box=>box.Categories.SelectMany(c=>c.Products)).Select(p=>p.Template))
            .Where(t=>t.WornSlot<=7).Select(t=>t.TemplateId).Distinct().ToArray();
        foreach(uint id in cloneIds)
        {
            // The legacy per-move preparer accepts up to four endpoints, not a whole catalog.
            try { clones.Prepare([id]); }
            catch(InvalidDataException) { /* Only supported frozen ordinary parts are exported. */ }
        }
        var townOffers = town.Select(r=>Product(r.Key,r.Value)).OfType<object>().ToList();
        if (commerce.SeriaExchange is { } seria)
        {
            templates[seria.Material.TemplateId]=seria.Material;
            foreach (byte job in Enumerable.Range(0,16).Select(n=>(byte)n))
                townOffers.Add(new {
                    Npc=2u, Shop=15u, Job=job, TemplateId=seria.Product.TemplateId, Gold=0u,
                    Template=seria.Product, Equipment=false, MinimumLevel=seria.Product.MinimumLevel,
                    Materials=new[]{new LocalNpcMaterialCost(seria.Material.TemplateId,seria.Cost)},
                    Limits=new[]{new LocalNpcPurchaseLimit(true,LocalCashPurchasePeriod.Daily,seria.Limit)}
                });
        }
        return new {
            commerce.AccountCargo,
            DailyExpiryTemplates=commerce.NpcPurchaseTemplates.Concat(commerce.BattlePassRewardTemplates)
                .Concat(coupons.Templates).Concat(keyedBoxes.Templates).Concat(luckyBoxes.Templates)
                .Concat(gifts.Templates.Values).Concat(items.PreparedTemplateIds.Select(items.Get).OfType<LocalItemTemplate>())
                .Concat(lottery.Templates).DistinctBy(t=>t.TemplateId)
                .Where(t=>t.Kind==LocalItemKind.Stackable&&t.DailyDeletion).ToDictionary(t=>t.TemplateId),
            TownPeers=peers.Select(p=>new{p.Key.Npc,p.Key.Shop,Placements=p.Value.Order().ToArray()}).ToArray(),
            Visibility=new { Hidden=Field(visibility,"_hidden"), Blocked=Field(visibility,"_blocked"), Rules=visibilityRows },
            TownAreas=areas,TownPhases=phases,TownMaps=maps,
            DungeonPeers=PairsAsPeers((HashSet<(uint Npc,uint Shop)>)Field(commerce,"_dungeonShops")),
            OculusPeers=((HashSet<(uint Dungeon,uint Npc,uint Shop)>)Field(commerce,"_oculusDungeonShops"))
                .OrderBy(p=>p.Dungeon).ThenBy(p=>p.Npc).ThenBy(p=>p.Shop).Select(p=>new{p.Dungeon,p.Npc,p.Shop}).ToArray(),
            Products=townOffers,
            DungeonProducts=dungeon.Select(r=>Product(r.Key,r.Value)).OfType<object>().ToArray(),
            SalePrices=sale, Templates=templates,
            PurchaseCodes=commerce.AccountPurchaseCodes,
            Gifts=commerce.GiftTemplateIds.Concat(ids).Distinct().Select(commerce.FindGiftBox).OfType<LocalGiftBoxDefinition>()
                .ToDictionary(d=>d.Template.TemplateId),
            CashPackages=Field(commerce,"_packages"),
            CargoGift=cargoGift.Source,
            ContractGrants=commerce.GiftTemplateIds.Concat(ids).Distinct().Where(id=>LocalContractRegistry.TryGet(id,out _))
                .ToDictionary(id=>id,id=>{LocalContractRegistry.TryGet(id,out var g);return g;}),
            Potions=ids.Append(8008u).Distinct().Select(items.GetPotion).OfType<LocalPotionDefinition>()
                .ToDictionary(p=>p.TemplateId),
            FatigueItems=ids.Concat(items.PreparedTemplateIds).Distinct().Select(items.GetFatigueItem)
                .OfType<LocalFatigueItemDefinition>().ToDictionary(p=>p.Template.TemplateId),
            Discard=ids.Select(items.GetDiscard).OfType<LocalInventoryDiscardDefinition>().ToDictionary(d=>d.Template),
            Disassemblers=Field(commerce,"_disassemblers"),
            Disassembly=Field(commerce,"_disassembly"),
            RepairNPCs=((HashSet<uint>)Field(commerce,"_repairNpcs")).Intersect((HashSet<uint>)Field(commerce,"_merchantNpcs")).ToArray(),
            RepairDefinitions=Field(commerce,"_repairItems"),
            SortMetadata=sortSources.Select(s=>new{s.TemplateId,s.ResourceHash,Info=sort.Get(s)})
                .Where(v=>v.Info is not null).DistinctBy(v=>v.TemplateId).ToDictionary(v=>v.TemplateId),
            AvatarOffers=avatarOffers,
            AvatarBoxes=avatarBoxes,
            AvatarClones=cloneIds.Select(clones.Get).OfType<LocalAvatarCloneTemplate>().ToDictionary(t=>t.Id),
            ContainerCoupons=LocalPvfContainerCouponCatalog.SourceIds.Select(coupons.Source)
                .OfType<LocalContainerCouponSource>().ToDictionary(t=>t.Template.TemplateId),
            MagicSeal=ids.Concat(items.PreparedTemplateIds).Distinct().Select(items.GetMagicSeal)
                .OfType<LocalMagicSealDefinition>().ToDictionary(t=>t.TemplateId),
            PetHatch=hatch,PetNames=petNames,PetRenameCard=commerce.PetRenames.GetCard(25),
            PetCare=commerce.PetCare.GetPet(100330838),PetFeed=commerce.PetCare.GetFeed(24),
            Lottery=lottery.Definitions.ToDictionary(r=>r.Key,r=>new {
                Source=r.Value.Gift.Template,r.Value.GoldCost,r.Value.ForbidScene8,r.Value.Gift.Materials,
                Prizes=r.Value.Gift.RandomPools.Single().Entries.Select(e=>new {e.Weight,e.Reward.TemplateId})
            }),
            KeyedBoxes=keyedRoots.Select(keyedBoxes.FindRandomBox).OfType<LocalRandomBoxDefinition>()
                .ToDictionary(d=>d.Source.TemplateId),
            LuckyBoxes=luckyRoots.Select(luckyBoxes.FindLuckyBox).OfType<LocalAncientHeroLuckyBoxDefinition>()
                .ToDictionary(d=>d.Source.TemplateId),
            LotteryTemplates=lottery.Templates.ToDictionary(t=>t.TemplateId),
            BoxWeights=boxWeightMap,
            Quality=quality,
            Craft=craft,
            GmApcs=apcs,
            FateExchange=fateExchange.Definition,
            FateBoxes=new {
                Stages=fate.Definition?.StageBoxes,
                StageHash=fate.Definition?.ResourceHash,
                StageReactivated=fate.Definition?.ArchivedEventReactivated??false,
                Exchange=fateBoxes.Definition?.Boxes,
                ExchangeHash=fateBoxes.Definition?.ResourceHash,
                ExchangeReactivated=fateBoxes.Definition?.ArchivedEventReactivated??false,
            },
            Activities=LocalServerActivities.Current,
            Cash=cash.Values.Where(AdmittedCash).ToDictionary(o=>o.Commodity,o=>new {
                o.Commodity,o.TemplateId,o.Count,o.Cera,o.Gold,o.TargetBagExpansion,o.TargetPersonalCargoCapacity,
                o.TargetAccountCargoCapacity,o.RevivalCoins,o.NativeFreePointOnly,o.AccountRestriction,o.AccountLimit,
                o.CharacterRestriction,o.CharacterDailyLimit,o.AccountPeriod,o.CharacterPeriod,o.BonusItems,o.OwnedItemExclusions,
                PreviousPersonalCargoCapacity=LocalCashCargoRules.Stages.FirstOrDefault(s=>s.Commodity==o.Commodity)?.PreviousCapacity??0,
                RequiredCouponTemplates=o.RequiredCouponTemplates.ToDictionary(t=>t.TemplateId),
                Template=templates.GetValueOrDefault(o.TemplateId),
                PremiumType=o.Premium?.Type??0,PremiumSeconds=o.Premium?.Seconds??0,
                CharacterSlot=o.CharacterSlot is not null,
                o.TargetAvatarExpansion,o.PetTemplate,o.NameTag,
                SkillStyleHash=o.SkillStyle?.ResourceHash,
                BattlePassHash=o.BattlePass?.Definition.ResourceHash,
                BattlePassDeluxe=o.BattlePass?.Deluxe??false
            })
        };

        object? Product((uint Npc,uint Shop,byte Job,uint Item) key,LocalNpcOffer offer)
        {
            if ((offer.Purchase?.Product ?? templates.GetValueOrDefault(offer.TemplateId)) is not {} t) return null;
            if (offer.Purchase is {} purchase)
                foreach (var payment in purchase.Payments) templates[payment.Template.TemplateId]=payment.Template;
            return new {
                key.Npc,key.Shop,key.Job,offer.TemplateId,offer.Equipment,Gold=offer.UnitPrice,Template=t,
                MinimumLevel=offer.Purchase?.MinimumLevel??0,
                Limits=offer.Purchase is {} q?LocalNpcPurchaseRules.Limits(q).ToArray():Array.Empty<LocalNpcPurchaseLimit>(),
                Materials=offer.Purchase is {} p?p.Payments.Select(r=>new LocalNpcMaterialCost(r.Template.TemplateId,r.Count)).ToArray():offer.Materials.ToArray()
            };
        }

        bool AdmittedCash(LocalCashOffer o)
        {
            LocalCashPaymentRules.RequireOffer(o);
            if (o.BattlePass is not null)
                return true;
            if(o.SkillStyle is not null)
            {
                LocalSkillStyles.RequireOffer(o);
                return true;
            }
            if(o.Premium is {} premium)
            {
                if(LocalDoubleJarService.Matches(o)||LocalQuestHelperService.Matches(o)||LocalFreeStaminaService.Matches(o)||
                    LocalFastJarService.Matches(o)||LocalSevenBuffService.Matches(o)||LocalHpMpRecoveryService.Matches(o)||
                    o.Commodity==LocalCharacterServiceRules.AutoRepairCommodity &&
                    o.TemplateId==LocalCharacterServiceRules.AutoRepairTemplate &&
                    o.Cera==LocalCharacterServiceRules.AutoRepairPrice &&
                    premium==new LocalPremiumOffer(LocalCharacterServiceRules.AutoRepairPurchaseType,
                        LocalPremiumRules.Duration,LocalCharacterServiceRules.AutoRepairHash)) return true;
                if(premium.Type is not (22 or 27 or 97)) return false;
                LocalPremiumRules.RequireOffer(o);
                return true;
            }
            if(o.CharacterSlot is not null)
            {
                LocalCharacterSlots.RequireOffer(o);
                return true;
            }
            if(o.NameTag is {} tag)
            {
                LocalCashNameTagRules.RequireOffer(tag);
                return true;
            }
            return o.RevivalCoins || o.TargetBagExpansion!=0 || o.TargetPersonalCargoCapacity!=0 ||
                o.TargetAccountCargoCapacity!=0 || o.TargetAvatarExpansion!=0 || o.PetTemplate is not null || templates.ContainsKey(o.TemplateId);
        }
    }

    private static IReadOnlyDictionary<uint,string> ReadGmApcs(PvfResourceProvider resources)
    {
        var input=resources.ReadIndex("aicharacter",1024*1024).Entries;
        var apcs=new LocalGmApcCatalog(resources);
        var definitions=new Dictionary<uint,string>();
        foreach(uint id in input.Keys.Where(id=>id is >0 and <=80000).Select(id=>(uint)id))
        {
            try
            {
                apcs.Prepare(id);
                if(apcs.Get(id) is {} row) definitions.Add(id,row.Hash);
            }
            catch(InvalidDataException) { }
            catch(IOException) { }
        }
        return definitions;
    }

    private static object ReadQuality(LocalCommerceCatalog commerce,LocalPvfItemTemplates items,
        PvfResourceProvider resources,IEnumerable<uint> roots)
    {
        var myth=new LocalOculusBossEquipmentCatalog(resources,items);
        myth.Prepare();
        var candidates=roots.Concat(items.PreparedTemplateIds).Distinct().ToArray();
        commerce.PrepareQualityBoxes(candidates,items,myth);
        var quality=commerce.QualityBoxes;
        var targets=candidates.Select(quality.Target).OfType<LocalItemTemplate>().ToDictionary(t=>t.TemplateId,t=>new {
            Template=t,Facts=quality.TargetFacts(t.TemplateId)
        });
        var profiles=Pairs(Field(myth,"_mythology")).Where(r=>targets.ContainsKey((uint)r.Key)).ToDictionary(r=>(uint)r.Key,r=>new {
            Hash=Property(r.Value,"Hash"),Count=Property(r.Value,"Count"),
            Options=((IEnumerable)Property(r.Value,"Options")).Cast<byte[]>().Select(a=>a.Select(b=>(int)b).ToArray()).ToArray()
        });
        return new { Sources=quality.Templates.ToDictionary(t=>t.TemplateId),Targets=targets,Mythology=profiles };
    }

    private static object ReadCraft(LocalCommerceCatalog commerce,LocalPvfItemTemplates items,
        PvfResourceProvider resources,IEnumerable<uint> roots,LocalPlayerMailPolicy weights,Dictionary<uint,long> weightMap)
    {
        var craft=new LocalEquipmentCraftCatalog(resources,commerce);
        craft.Prepare(roots.Concat(items.PreparedTemplateIds).Concat(LocalEquipmentCraftCatalog.TicketTemplateIds));
        var equipment=Pairs(Field(craft,"_equipment")).Select(p=>(LocalEquipmentCraftEquipmentDefinition)p.Value).ToArray();
        var quotes=new List<object>();
        var groups=new Dictionary<(int Min,int Grade,int Rarity,short Slot,int Remodel),int>();
        var definitions=new Dictionary<uint,object>();
        foreach(var e in equipment)
        {
            var key=(e.MinimumLevel,e.Grade,e.Rarity,e.TargetEquipmentSlot,e.RemodelGroup);
            if(!groups.TryGetValue(key,out int index))
            {
                index=quotes.Count;
                groups.Add(key,index);
                quotes.Add(new {
                    Reinforce=QuoteStages(31,n=>craft.QuoteUpgrade(e,n,LocalEquipmentCraftMethod.Reinforce)),
                    Amplify=QuoteStages(31,n=>craft.QuoteUpgrade(e,n,LocalEquipmentCraftMethod.Amplify)),
                    Forge=QuoteStages(8,n=>craft.QuoteForge(e,n)),
                    Remodel=e.RemodelGroup==0?[]:QuoteStages(31,n=>craft.QuoteUpgrade(e,n,LocalEquipmentCraftMethod.Remodel))
                });
            }
            definitions.Add(e.TemplateId,new { Equipment=e, Quote=index });
        }
        var materials=craft.TemplateIds.ToArray();
        items.Prepare(materials);
        foreach(uint id in materials)
        {
            try
            {
                weights.Prepare([id]);
                weightMap.TryAdd(id,weights.ItemWeight(id));
            }
            catch(Exception ex) when(ex is InvalidDataException or FileNotFoundException) { }
        }
        var tickets=Pairs(Field(craft,"_tickets")).Select(p=>(LocalEquipmentTicketDefinition)p.Value).ToDictionary(t=>t.TemplateId,t=>new {
            t.TemplateId,t.Kind,t.Level,t.ResourceHash,t.ChancePercent,t.BookAction,t.BookOption,t.BookMaterialCount,
            t.BookMaximumUpgradeLevel,t.BookMinimumLevel,
            RandomValues=t.RandomValues.Select(v=>new {v.Value,v.Weight}),
            BookTypeWeights=t.BookTypeWeights.Select(v=>new {v.Value,v.Weight})
        });
        var destruction=Pairs(Field(craft,"_destructionDefinitions")).ToDictionary(p=>(uint)p.Key,p=>new {
            CubeId=Property(p.Value,"CubeId"),CubeCount=Property(p.Value,"CubeCount"),Row=Property(p.Value,"Row"),
            SuppressSoul=Property(p.Value,"SuppressSoul"),
            Fixed=p.Value.GetType().GetProperty("Fixed")!.GetValue(p.Value)
        });
        return new {
            Definitions=definitions,Quotes=quotes,Tickets=tickets,Destruction=destruction,Enchants=Field(craft,"_enchants"),
            NPCs=Pairs(Field(commerce,"_craftNpcs")).ToDictionary(p=>Convert.ToInt32(p.Key),p=>p.Value),
            Templates=materials.Select(items.Get).OfType<LocalItemTemplate>().ToDictionary(t=>t.TemplateId)
        };

        static object?[] QuoteStages(int count,Func<byte,LocalEquipmentCraftQuote> resolve)
        {
            var rows=new object?[count];
            for(byte n=0;n<count;n++)
            {
                try
                {
                    var q=resolve(n);
                    rows[n]=new { q.Gold,q.MaterialId,q.MaterialCount,q.FailureWeight,q.PenaltyType,q.PenaltyLoss,
                        q.DestroyRewards,q.ResourceHash,RemodelPenalties=q.RemodelPenalties.Select(r=>new {r.Loss,r.Weight}) };
                }
                catch(InvalidDataException) { }
                catch(OverflowException) { }
            }
            return rows;
        }
    }

    private static object[] PairsAsPeers(IEnumerable<(uint Npc,uint Shop)> rows) =>
        rows.Select(r=>(object)new{r.Npc,r.Shop}).ToArray();
}
